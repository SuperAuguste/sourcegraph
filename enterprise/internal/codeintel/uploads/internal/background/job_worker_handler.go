package background

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/jackc/pgconn"
	"github.com/keegancsmith/sqlf"
	otlog "github.com/opentracing/opentracing-go/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sourcegraph/log"
	"go.opentelemetry.io/otel/attribute"

	codeinteltypes "github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/shared/types"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/uploads/internal/lsifstore"
	"github.com/sourcegraph/sourcegraph/enterprise/internal/codeintel/uploads/internal/store"
	"github.com/sourcegraph/sourcegraph/internal/actor"
	"github.com/sourcegraph/sourcegraph/internal/api"
	"github.com/sourcegraph/sourcegraph/internal/gitserver/gitdomain"
	"github.com/sourcegraph/sourcegraph/internal/observation"
	"github.com/sourcegraph/sourcegraph/internal/types"
	"github.com/sourcegraph/sourcegraph/internal/uploadstore"
	"github.com/sourcegraph/sourcegraph/internal/workerutil"
	"github.com/sourcegraph/sourcegraph/internal/workerutil/dbworker"
	dbworkerstore "github.com/sourcegraph/sourcegraph/internal/workerutil/dbworker/store"
	"github.com/sourcegraph/sourcegraph/lib/codeintel/lsif/conversion"
	"github.com/sourcegraph/sourcegraph/lib/codeintel/precise"
	"github.com/sourcegraph/sourcegraph/lib/errors"
)

func NewUploadProcessorWorker(
	observationCtx *observation.Context,
	store store.Store,
	lsifstore lsifstore.LsifStore,
	gitserverClient GitserverClient,
	repoStore RepoStore,
	workerStore dbworkerstore.Store[codeinteltypes.Upload],
	uploadStore uploadstore.Store,
	workerConcurrency int,
	workerBudget int64,
	workerPollInterval time.Duration,
	maximumRuntimePerJob time.Duration,
) *workerutil.Worker[codeinteltypes.Upload] {
	rootContext := actor.WithInternalActor(context.Background())

	handler := NewUploadProcessorHandler(
		observationCtx,
		store,
		lsifstore,
		gitserverClient,
		repoStore,
		workerStore,
		uploadStore,
		workerConcurrency,
		workerBudget,
	)

	metrics := workerutil.NewMetrics(observationCtx, "codeintel_upload_processor", workerutil.WithSampler(func(job workerutil.Record) bool { return true }))

	return dbworker.NewWorker(rootContext, workerStore, handler, workerutil.WorkerOptions{
		Name:                 "precise_code_intel_upload_worker",
		Description:          "processes precise code-intel uploads",
		NumHandlers:          workerConcurrency,
		Interval:             workerPollInterval,
		HeartbeatInterval:    time.Second,
		Metrics:              metrics,
		MaximumRuntimePerJob: maximumRuntimePerJob,
	})
}

type handler struct {
	store           store.Store
	lsifstore       lsifstore.LsifStore
	gitserverClient GitserverClient
	repoStore       RepoStore
	workerStore     dbworkerstore.Store[codeinteltypes.Upload]
	uploadStore     uploadstore.Store
	handleOp        *observation.Operation
	budgetRemaining int64
	enableBudget    bool
	uploadSizeGuage prometheus.Gauge
}

var (
	_ workerutil.Handler[codeinteltypes.Upload]   = &handler{}
	_ workerutil.WithPreDequeue                   = &handler{}
	_ workerutil.WithHooks[codeinteltypes.Upload] = &handler{}
)

func NewUploadProcessorHandler(
	observationCtx *observation.Context,
	store store.Store,
	lsifstore lsifstore.LsifStore,
	gitserverClient GitserverClient,
	repoStore RepoStore,
	workerStore dbworkerstore.Store[codeinteltypes.Upload],
	uploadStore uploadstore.Store,
	numProcessorRoutines int,
	budgetMax int64,
) workerutil.Handler[codeinteltypes.Upload] {
	operations := newWorkerOperations(observationCtx)

	return &handler{
		store:           store,
		lsifstore:       lsifstore,
		gitserverClient: gitserverClient,
		repoStore:       repoStore,
		workerStore:     workerStore,
		uploadStore:     uploadStore,
		handleOp:        operations.uploadProcessor,
		budgetRemaining: budgetMax,
		enableBudget:    budgetMax > 0,
		uploadSizeGuage: operations.uploadSizeGuage,
	}
}

func (h *handler) Handle(ctx context.Context, logger log.Logger, upload codeinteltypes.Upload) (err error) {
	var requeued bool

	ctx, otLogger, endObservation := h.handleOp.With(ctx, &err, observation.Args{})
	defer func() {
		endObservation(1, observation.Args{
			LogFields: append(
				createLogFields(upload),
				otlog.Bool("requeued", requeued),
			),
		})
	}()

	requeued, err = h.HandleRawUpload(ctx, logger, upload, h.uploadStore, otLogger)

	return err
}

func (h *handler) PreDequeue(ctx context.Context, logger log.Logger) (bool, any, error) {
	if !h.enableBudget {
		return true, nil, nil
	}

	budgetRemaining := atomic.LoadInt64(&h.budgetRemaining)
	if budgetRemaining <= 0 {
		return false, nil, nil
	}

	return true, []*sqlf.Query{sqlf.Sprintf("(upload_size IS NULL OR upload_size <= %s)", budgetRemaining)}, nil
}

func (h *handler) PreHandle(ctx context.Context, logger log.Logger, upload codeinteltypes.Upload) {
	uncompressedSize := h.getUploadSize(upload.UncompressedSize)
	h.uploadSizeGuage.Add(float64(uncompressedSize))

	gzipSize := h.getUploadSize(upload.UploadSize)
	atomic.AddInt64(&h.budgetRemaining, -gzipSize)
}

func (h *handler) PostHandle(ctx context.Context, logger log.Logger, upload codeinteltypes.Upload) {
	uncompressedSize := h.getUploadSize(upload.UncompressedSize)
	h.uploadSizeGuage.Sub(float64(uncompressedSize))

	gzipSize := h.getUploadSize(upload.UploadSize)
	atomic.AddInt64(&h.budgetRemaining, +gzipSize)
}

func (h *handler) getUploadSize(field *int64) int64 {
	if field != nil {
		return *field
	}

	return 0
}

func createLogFields(upload codeinteltypes.Upload) []otlog.Field {
	fields := []otlog.Field{
		otlog.Int("uploadID", upload.ID),
		otlog.Int("repositoryID", upload.RepositoryID),
		otlog.String("commit", upload.Commit),
		otlog.String("root", upload.Root),
		otlog.String("indexer", upload.Indexer),
		otlog.Int("queueDuration", int(time.Since(upload.UploadedAt))),
	}

	if upload.UploadSize != nil {
		fields = append(fields, otlog.Int64("uploadSize", *upload.UploadSize))
	}

	return fields
}

// handle converts a raw upload into a dump within the given transaction context. Returns true if the
// upload record was requeued and false otherwise.
func (s *handler) HandleRawUpload(ctx context.Context, logger log.Logger, upload codeinteltypes.Upload, uploadStore uploadstore.Store, trace observation.TraceLogger) (requeued bool, err error) {
	repo, err := s.repoStore.Get(ctx, api.RepoID(upload.RepositoryID))
	if err != nil {
		return false, errors.Wrap(err, "Repos.Get")
	}

	if requeued, err := requeueIfCloningOrCommitUnknown(ctx, logger, s.repoStore, s.workerStore, upload, repo); err != nil || requeued {
		return requeued, err
	}

	// Determine if the upload is for the default Git branch.
	isDefaultBranch, err := s.gitserverClient.DefaultBranchContains(ctx, upload.RepositoryID, upload.Commit)
	if err != nil {
		return false, errors.Wrap(err, "gitserver.DefaultBranchContains")
	}

	trace.AddEvent("TODO Domain Owner", attribute.Bool("defaultBranch", isDefaultBranch))

	getChildren := func(ctx context.Context, dirnames []string) (map[string][]string, error) {
		directoryChildren, err := s.gitserverClient.DirectoryChildren(ctx, upload.RepositoryID, upload.Commit, dirnames)
		if err != nil {
			return nil, errors.Wrap(err, "gitserverClient.DirectoryChildren")
		}
		return directoryChildren, nil
	}

	return false, withUploadData(ctx, logger, uploadStore, upload.ID, trace, func(r io.Reader) (err error) {
		const (
			lsifContentType = "application/x-ndjson+lsif"
			scipContentType = "application/x-protobuf+scip"
		)
		var (
			groupedBundleData  *precise.GroupedBundleDataChans
			correlatedSCIPData lsifstore.ProcessedSCIPData
		)
		if upload.ContentType == lsifContentType {
			if groupedBundleData, err = conversion.Correlate(ctx, r, upload.Root, getChildren); err != nil {
				return errors.Wrap(err, "conversion.Correlate")
			}
		} else if upload.ContentType == scipContentType {
			if correlatedSCIPData, err = correlateSCIP(ctx, r, upload.Root, getChildren); err != nil {
				return errors.Wrap(err, "conversion.Correlate")
			}
		} else {
			return errors.Newf("unsupported content type %q", upload.ContentType)
		}

		// Find the commit date for the commit attached to this upload record and insert it into the
		// database (if not already present). We need to have the commit data of every processed upload
		// for a repository when calculating the commit graph (triggered at the end of this handler).

		_, commitDate, revisionExists, err := s.gitserverClient.CommitDate(ctx, upload.RepositoryID, upload.Commit)
		if err != nil {
			return errors.Wrap(err, "gitserverClient.CommitDate")
		}
		if !revisionExists {
			return errCommitDoesNotExist
		}
		trace.AddEvent("TODO Domain Owner", attribute.String("commitDate", commitDate.String()))

		// We do the update here outside of the transaction started below to reduce the long blocking
		// behavior we see when multiple uploads are being processed for the same repository and commit.
		// We do choose to perform this before this the following transaction rather than after so that
		// we can guarantee the presence of the date for this commit by the time the repository is set
		// as dirty.
		if err := s.store.UpdateCommittedAt(ctx, upload.RepositoryID, upload.Commit, commitDate.Format(time.RFC3339)); err != nil {
			return errors.Wrap(err, "store.CommitDate")
		}

		if upload.ContentType == lsifContentType {
			// Note: this is writing to a different database than the block below, so we need to use a
			// different transaction context (managed by the writeData function).
			if err := writeData(ctx, s.lsifstore, upload, groupedBundleData, trace); err != nil {
				if isUniqueConstraintViolation(err) {
					// If this is a unique constraint violation, then we've previously processed this same
					// upload record up to this point, but failed to perform the transaction below. We can
					// safely assume that the entire index's data is in the codeintel database, as it's
					// parsed deterministically and written atomically.
					logger.Warn("LSIF data already exists for upload record")
					trace.AddEvent("TODO Domain Owner", attribute.Bool("rewriting", true))
				} else {
					return err
				}
			}
		} else if upload.ContentType == scipContentType {
			// Note: this is writing to a different database than the block below, so we need to use a
			// different transaction context (managed by the writeData function).
			if err := writeSCIPData(ctx, s.lsifstore, upload, correlatedSCIPData, trace); err != nil {
				if isUniqueConstraintViolation(err) {
					// If this is a unique constraint violation, then we've previously processed this same
					// upload record up to this point, but failed to perform the transaction below. We can
					// safely assume that the entire index's data is in the codeintel database, as it's
					// parsed deterministically and written atomically.
					logger.Warn("SCIP data already exists for upload record")
					trace.AddEvent("TODO Domain Onwer", attribute.Bool("rewriting", true))
				} else {
					return err
				}
			}
		}

		// Start a nested transaction with Postgres savepoints. In the event that something after this
		// point fails, we want to update the upload record with an error message but do not want to
		// alter any other data in the database. Rolling back to this savepoint will allow us to discard
		// any other changes but still commit the transaction as a whole.
		return inTransaction(ctx, s.store, func(tx store.Store) error {
			// Before we mark the upload as complete, we need to delete any existing completed uploads
			// that have the same repository_id, commit, root, and indexer values. Otherwise the transaction
			// will fail as these values form a unique constraint.
			if err := tx.DeleteOverlappingDumps(ctx, upload.RepositoryID, upload.Commit, upload.Root, upload.Indexer); err != nil {
				return errors.Wrap(err, "store.DeleteOverlappingDumps")
			}

			if upload.ContentType == lsifContentType {
				trace.AddEvent("TODO Domain Owner", attribute.Int("packages", len(groupedBundleData.Packages)))
				// Update package and package reference data to support cross-repo queries.
				if err := tx.UpdatePackages(ctx, upload.ID, groupedBundleData.Packages); err != nil {
					return errors.Wrap(err, "store.UpdatePackages")
				}
				trace.AddEvent("TODO Domain Owner", attribute.Int("packageReferences", len(groupedBundleData.Packages)))
				if err := tx.UpdatePackageReferences(ctx, upload.ID, groupedBundleData.PackageReferences); err != nil {
					return errors.Wrap(err, "store.UpdatePackageReferences")
				}
			} else if upload.ContentType == scipContentType {
				packages, packageReferences, err := readPackageAndPackageReferences(ctx, correlatedSCIPData)
				if err != nil {
					return err
				}

				trace.AddEvent("TODO Domain Owner", attribute.Int("packages", len(packages)))
				// Update package and package reference data to support cross-repo queries.
				if err := tx.UpdatePackages(ctx, upload.ID, packages); err != nil {
					return errors.Wrap(err, "store.UpdatePackages")
				}
				trace.AddEvent("TODO Domain Owner", attribute.Int("packageReferences", len(packages)))
				if err := tx.UpdatePackageReferences(ctx, upload.ID, packageReferences); err != nil {
					return errors.Wrap(err, "store.UpdatePackageReferences")
				}
			}

			// Insert a companion record to this upload that will asynchronously trigger other workers to
			// sync/create referenced dependency repositories and queue auto-index records for the monikers
			// written into the lsif_references table attached by this index processing job.
			if _, err := tx.InsertDependencySyncingJob(ctx, upload.ID); err != nil {
				return errors.Wrap(err, "store.InsertDependencyIndexingJob")
			}

			// Mark this repository so that the commit updater process will pull the full commit graph from
			// gitserver and recalculate the nearest upload for each commit as well as which uploads are visible
			// from the tip of the default branch. We don't do this inside of the transaction as we re-calcalute
			// the entire set of data from scratch and we want to be able to coalesce requests for the same
			// repository rather than having a set of uploads for the same repo re-calculate nearly identical
			// data multiple times.
			if err := tx.SetRepositoryAsDirty(ctx, upload.RepositoryID); err != nil {
				return errors.Wrap(err, "store.MarkRepositoryAsDirty")
			}

			return nil
		})
	})
}

func inTransaction(ctx context.Context, dbStore store.Store, fn func(tx store.Store) error) (err error) {
	tx, err := dbStore.Transact(ctx)
	if err != nil {
		return errors.Wrap(err, "store.Transact")
	}
	defer func() { err = tx.Done(err) }()

	return fn(tx)
}

// requeueDelay is the delay between processing attempts to process a record when waiting on
// gitserver to refresh. We'll requeue a record with this delay while the repo is cloning or
// while we're waiting for a commit to become available to the remote code host.
const requeueDelay = time.Minute

// requeueIfCloningOrCommitUnknown ensures that the repo and revision are resolvable. If the repo is currently
// cloning or if the commit does not exist, then the upload will be requeued and this function returns a true
// valued flag. Otherwise, the repo does not exist or there is an unexpected infrastructure error, which we'll
// fail on.
func requeueIfCloningOrCommitUnknown(ctx context.Context, logger log.Logger, repoStore RepoStore, workerStore dbworkerstore.Store[codeinteltypes.Upload], upload codeinteltypes.Upload, repo *types.Repo) (requeued bool, _ error) {
	_, err := repoStore.ResolveRev(ctx, repo, upload.Commit)
	if err == nil {
		// commit is resolvable
		return false, nil
	}

	var reason string
	if errors.HasType(err, &gitdomain.RevisionNotFoundError{}) {
		reason = "commit not found"
	} else if gitdomain.IsCloneInProgress(err) {
		reason = "repository still cloning"
	} else {
		return false, errors.Wrap(err, "repos.ResolveRev")
	}

	after := time.Now().UTC().Add(requeueDelay)

	if err := workerStore.Requeue(ctx, upload.ID, after); err != nil {
		return false, errors.Wrap(err, "store.Requeue")
	}
	logger.Warn("Requeued LSIF upload record",
		log.Int("id", upload.ID),
		log.String("reason", reason))
	return true, nil
}

// withUploadData will invoke the given function with a reader of the upload's raw data. The
// consumer should expect raw newline-delimited JSON content. If the function returns without
// an error, the upload file will be deleted.
func withUploadData(ctx context.Context, logger log.Logger, uploadStore uploadstore.Store, id int, trace observation.TraceLogger, fn func(r io.Reader) error) error {
	uploadFilename := fmt.Sprintf("upload-%d.lsif.gz", id)

	trace.AddEvent("TODO Domain Owner", attribute.String("uploadFilename", uploadFilename))

	// Pull raw uploaded data from bucket
	rc, err := uploadStore.Get(ctx, uploadFilename)
	if err != nil {
		return errors.Wrap(err, "uploadStore.Get")
	}
	defer rc.Close()

	rc, err = gzip.NewReader(rc)
	if err != nil {
		return errors.Wrap(err, "gzip.NewReader")
	}
	defer rc.Close()

	if err := fn(rc); err != nil {
		return err
	}

	if err := uploadStore.Delete(ctx, uploadFilename); err != nil {
		logger.Warn("Failed to delete upload file",
			log.NamedError("err", err),
			log.String("filename", uploadFilename))
	}

	return nil
}

// writeData transactionally writes the given grouped bundle data into the given LSIF store.
func writeData(ctx context.Context, lsifStore lsifstore.LsifStore, upload codeinteltypes.Upload, groupedBundleData *precise.GroupedBundleDataChans, trace observation.TraceLogger) (err error) {
	tx, err := lsifStore.Transact(ctx)
	if err != nil {
		return err
	}
	defer func() { err = tx.Done(err) }()

	if err := tx.WriteMeta(ctx, upload.ID, groupedBundleData.Meta); err != nil {
		return errors.Wrap(err, "store.WriteMeta")
	}
	count, err := tx.WriteDocuments(ctx, upload.ID, groupedBundleData.Documents)
	if err != nil {
		return errors.Wrap(err, "store.WriteDocuments")
	}
	trace.AddEvent("TODO Domain Owner", attribute.Int64("numDocuments", int64(count)))

	count, err = tx.WriteResultChunks(ctx, upload.ID, groupedBundleData.ResultChunks)
	if err != nil {
		return errors.Wrap(err, "store.WriteResultChunks")
	}
	trace.AddEvent("TODO Domain Owner", attribute.Int64("numResultChunks", int64(count)))

	count, err = tx.WriteDefinitions(ctx, upload.ID, groupedBundleData.Definitions)
	if err != nil {
		return errors.Wrap(err, "store.WriteDefinitions")
	}
	trace.AddEvent("TODO Domain Owner", attribute.Int64("numDefinitions", int64(count)))

	count, err = tx.WriteReferences(ctx, upload.ID, groupedBundleData.References)
	if err != nil {
		return errors.Wrap(err, "store.WriteReferences")
	}
	trace.AddEvent("TODO Domain Owner", attribute.Int64("numReferences", int64(count)))

	count, err = tx.WriteImplementations(ctx, upload.ID, groupedBundleData.Implementations)
	if err != nil {
		return errors.Wrap(err, "store.WriteImplementations")
	}
	trace.AddEvent("TODO Domain Owner", attribute.Int64("numImplementations", int64(count)))

	return nil
}

func isUniqueConstraintViolation(err error) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && e.Code == "23505"
}

// errCommitDoesNotExist occurs when gitserver does not recognize the commit attached to the upload.
var errCommitDoesNotExist = errors.Errorf("commit does not exist")
