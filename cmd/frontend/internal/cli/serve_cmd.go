package cli

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/graph-gophers/graphql-go"
	"github.com/keegancsmith/tmpfriend"
	sglog "github.com/sourcegraph/log"
	"github.com/throttled/throttled/v2"
	"github.com/throttled/throttled/v2/store/memstore"
	"github.com/throttled/throttled/v2/store/redigostore"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/enterprise"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/envvar"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/globals"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/graphqlbackend"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/app/ui"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/app/updatecheck"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/bg"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/cli/loghandlers"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/httpapi"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/siteid"
	oce "github.com/sourcegraph/sourcegraph/cmd/frontend/oneclickexport"
	"github.com/sourcegraph/sourcegraph/internal/adminanalytics"
	"github.com/sourcegraph/sourcegraph/internal/conf"
	"github.com/sourcegraph/sourcegraph/internal/conf/conftypes"
	"github.com/sourcegraph/sourcegraph/internal/conf/deploy"
	"github.com/sourcegraph/sourcegraph/internal/database"
	connections "github.com/sourcegraph/sourcegraph/internal/database/connections/live"
	"github.com/sourcegraph/sourcegraph/internal/debugserver"
	"github.com/sourcegraph/sourcegraph/internal/encryption/keyring"
	"github.com/sourcegraph/sourcegraph/internal/env"
	"github.com/sourcegraph/sourcegraph/internal/gitserver"
	"github.com/sourcegraph/sourcegraph/internal/goroutine"
	"github.com/sourcegraph/sourcegraph/internal/hostname"
	"github.com/sourcegraph/sourcegraph/internal/httpserver"
	"github.com/sourcegraph/sourcegraph/internal/logging"
	"github.com/sourcegraph/sourcegraph/internal/observation"
	"github.com/sourcegraph/sourcegraph/internal/oobmigration"
	"github.com/sourcegraph/sourcegraph/internal/profiler"
	"github.com/sourcegraph/sourcegraph/internal/redispool"
	"github.com/sourcegraph/sourcegraph/internal/sysreq"
	"github.com/sourcegraph/sourcegraph/internal/tracer"
	"github.com/sourcegraph/sourcegraph/internal/users"
	"github.com/sourcegraph/sourcegraph/internal/version"
	"github.com/sourcegraph/sourcegraph/internal/version/upgradestore"
	"github.com/sourcegraph/sourcegraph/lib/errors"
)

var (
	traceFields    = env.Get("SRC_LOG_TRACE", "HTTP", "space separated list of trace logs to show. Options: all, HTTP, build, github")
	traceThreshold = env.Get("SRC_LOG_TRACE_THRESHOLD", "", "show traces that take longer than this")

	printLogo, _ = strconv.ParseBool(env.Get("LOGO", "false", "print Sourcegraph logo upon startup"))

	httpAddr = env.Get("SRC_HTTP_ADDR", func() string {
		if env.InsecureDev {
			return "127.0.0.1:3080"
		}
		return ":3080"
	}(), "HTTP listen address for app and HTTP API")
	httpAddrInternal = envvar.HTTPAddrInternal

	nginxAddr = env.Get("SRC_NGINX_HTTP_ADDR", "", "HTTP listen address for nginx reverse proxy to SRC_HTTP_ADDR. Has preference over SRC_HTTP_ADDR for ExternalURL.")

	// dev browser extension ID. You can find this by going to chrome://extensions
	devExtension = "chrome-extension://bmfbcejdknlknpncfpeloejonjoledha"
	// production browser extension ID. This is found by viewing our extension in the chrome store.
	prodExtension = "chrome-extension://dgjhfomjieaadpoljlnidmbgkdffpack"
)

// InitDB initializes and returns the global database connection and sets the
// version of the frontend in our versions table.
func InitDB(logger sglog.Logger) (*sql.DB, error) {
	sqlDB, err := connections.EnsureNewFrontendDB(observation.ContextWithLogger(logger, &observation.TestContext), "", "frontend")
	if err != nil {
		return nil, errors.Errorf("failed to connect to frontend database: %s", err)
	}

	if err := upgradestore.New(database.NewDB(logger, sqlDB)).UpdateServiceVersion(context.Background(), "frontend", version.Version()); err != nil {
		return nil, err
	}

	return sqlDB, nil
}

// Main is the main entrypoint for the frontend server program.
func Main(enterpriseSetupHook func(database.DB, conftypes.UnifiedWatchable) enterprise.Services) error {
	ctx := context.Background()

	log.SetFlags(0)
	log.SetPrefix("")

	liblog := sglog.Init(sglog.Resource{
		Name:       env.MyName,
		Version:    version.Version(),
		InstanceID: hostname.Get(),
	}, sglog.NewSentrySinkWith(
		sglog.SentrySink{
			ClientOptions: sentry.ClientOptions{SampleRate: 0.2},
		},
	)) // Experimental: DevX is observing how sampling affects the errors signal
	defer liblog.Sync()

	logger := sglog.Scoped("server", "the frontend server program")
	ready := make(chan struct{})
	go debugserver.NewServerRoutine(ready).Start()

	sqlDB, err := InitDB(logger)
	if err != nil {
		return err
	}
	db := database.NewDB(logger, sqlDB)

	observationCtx := observation.NewContext(logger)

	if os.Getenv("SRC_DISABLE_OOBMIGRATION_VALIDATION") != "" {
		logger.Warn("Skipping out-of-band migrations check")
	} else {
		outOfBandMigrationRunner := oobmigration.NewRunnerWithDB(observationCtx, db, oobmigration.RefreshInterval)

		if err := outOfBandMigrationRunner.SynchronizeMetadata(ctx); err != nil {
			return errors.Wrap(err, "failed to synchronized out of band migration metadata")
		}

		if err := oobmigration.ValidateOutOfBandMigrationRunner(ctx, db, outOfBandMigrationRunner); err != nil {
			return errors.Wrap(err, "failed to validate out of band migrations")
		}
	}

	// override site config first
	if err := overrideSiteConfig(ctx, logger, db); err != nil {
		return errors.Wrap(err, "failed to apply site config overrides")
	}
	globals.ConfigurationServerFrontendOnly = conf.InitConfigurationServerFrontendOnly(newConfigurationSource(logger, db))
	conf.Init()
	conf.MustValidateDefaults()
	go conf.Watch(liblog.Update(conf.GetLogSinks))

	// now we can init the keyring, as it depends on site config
	if err := keyring.Init(ctx); err != nil {
		return errors.Wrap(err, "failed to initialize encryption keyring")
	}

	if err := overrideGlobalSettings(ctx, logger, db); err != nil {
		return errors.Wrap(err, "failed to override global settings")
	}

	// now the keyring is configured it's safe to override the rest of the config
	// and that config can access the keyring
	if err := overrideExtSvcConfig(ctx, logger, db); err != nil {
		return errors.Wrap(err, "failed to override external service config")
	}

	// Filter trace logs
	d, _ := time.ParseDuration(traceThreshold)
	logging.Init(logging.Filter(loghandlers.Trace(strings.Fields(traceFields), d))) //nolint:staticcheck // Deprecated, but logs unmigrated to sourcegraph/log look really bad without this.
	tracer.Init(sglog.Scoped("tracer", "internal tracer package"), conf.DefaultClient())
	profiler.Init()

	// Run enterprise setup hook
	enterprise := enterpriseSetupHook(db, conf.DefaultClient())

	if err != nil {
		return errors.Wrap(err, "Failed to create sub-repo client")
	}
	ui.InitRouter(db)

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "help", "-h", "--help":
			log.Printf("Version: %s", version.Version())
			log.Print()

			log.Print(env.HelpString())

			log.Print()
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for _, st := range sysreq.Check(ctx, skippedSysReqs()) {
				log.Printf("%s:", st.Name)
				if st.OK() {
					log.Print("\tOK")
					continue
				}
				if st.Skipped {
					log.Print("\tSkipped")
					continue
				}
				if st.Problem != "" {
					log.Print("\t" + st.Problem)
				}
				if st.Err != nil {
					log.Printf("\tError: %s", st.Err)
				}
				if st.Fix != "" {
					log.Printf("\tPossible fix: %s", st.Fix)
				}
			}

			return nil
		}
	}

	printConfigValidation(logger)

	cleanup := tmpfriend.SetupOrNOOP()
	defer cleanup()

	// Don't proceed if system requirements are missing, to avoid
	// presenting users with a half-working experience.
	if err := checkSysReqs(context.Background(), os.Stderr); err != nil {
		return err
	}

	siteid.Init(db)

	globals.WatchBranding()
	globals.WatchExternalURL()
	globals.WatchPermissionsUserMapping()

	goroutine.Go(func() { bg.CheckRedisCacheEvictionPolicy() })
	goroutine.Go(func() { bg.DeleteOldCacheDataInRedis() })
	goroutine.Go(func() { bg.DeleteOldEventLogsInPostgres(context.Background(), db) })
	goroutine.Go(func() { bg.DeleteOldSecurityEventLogsInPostgres(context.Background(), db) })
	goroutine.Go(func() { bg.UpdatePermissions(ctx, logger, db) })
	goroutine.Go(func() { updatecheck.Start(logger, db) })
	goroutine.Go(func() { adminanalytics.StartAnalyticsCacheRefresh(context.Background(), db) })
	goroutine.Go(func() { users.StartUpdateAggregatedUsersStatisticsTable(context.Background(), db) })

	schema, err := graphqlbackend.NewSchema(db,
		gitserver.NewClient(db),
		enterprise.BatchChangesResolver,
		enterprise.CodeIntelResolver,
		enterprise.InsightsResolver,
		enterprise.AuthzResolver,
		enterprise.CodeMonitorsResolver,
		enterprise.LicenseResolver,
		enterprise.DotcomResolver,
		enterprise.SearchContextsResolver,
		enterprise.NotebooksResolver,
		enterprise.ComputeResolver,
		enterprise.InsightsAggregationResolver,
		enterprise.WebhooksResolver,
	)
	if err != nil {
		return err
	}

	rateLimitWatcher, err := makeRateLimitWatcher()
	if err != nil {
		return err
	}

	server, err := makeExternalAPI(db, logger, schema, enterprise, rateLimitWatcher)
	if err != nil {
		return err
	}

	internalAPI, err := makeInternalAPI(db, logger, schema, enterprise, rateLimitWatcher)
	if err != nil {
		return err
	}

	routines := []goroutine.BackgroundRoutine{server}
	if internalAPI != nil {
		routines = append(routines, internalAPI)
	}

	oce.GlobalExporter = oce.NewDataExporter(db, logger)

	if printLogo {
		// This is not a log entry and is usually disabled
		println(fmt.Sprintf("\n\n%s\n\n", logoColor))
	}
	logger.Info(fmt.Sprintf("✱ Sourcegraph is ready at: %s", globals.ExternalURL()))
	close(ready)

	goroutine.MonitorBackgroundRoutines(context.Background(), routines...)
	return nil
}

func makeExternalAPI(db database.DB, logger sglog.Logger, schema *graphql.Schema, enterprise enterprise.Services, rateLimiter graphqlbackend.LimitWatcher) (goroutine.BackgroundRoutine, error) {
	listener, err := httpserver.NewListener(httpAddr)
	if err != nil {
		return nil, err
	}

	// Create the external HTTP handler.
	externalHandler := newExternalHTTPHandler(
		db,
		schema,
		rateLimiter,
		&httpapi.Handlers{
			GitHubSyncWebhook:               enterprise.ReposGithubWebhook,
			GitLabSyncWebhook:               enterprise.ReposGitLabWebhook,
			BitbucketServerSyncWebhook:      enterprise.ReposBitbucketServerWebhook,
			BitbucketCloudSyncWebhook:       enterprise.ReposBitbucketCloudWebhook,
			PermissionsGitHubWebhook:        enterprise.PermissionsGitHubWebhook,
			BatchesGitHubWebhook:            enterprise.BatchesGitHubWebhook,
			BatchesGitLabWebhook:            enterprise.BatchesGitLabWebhook,
			BatchesBitbucketServerWebhook:   enterprise.BatchesBitbucketServerWebhook,
			BatchesBitbucketCloudWebhook:    enterprise.BatchesBitbucketCloudWebhook,
			BatchesChangesFileGetHandler:    enterprise.BatchesChangesFileGetHandler,
			BatchesChangesFileExistsHandler: enterprise.BatchesChangesFileExistsHandler,
			BatchesChangesFileUploadHandler: enterprise.BatchesChangesFileUploadHandler,
			NewCodeIntelUploadHandler:       enterprise.NewCodeIntelUploadHandler,
			NewComputeStreamHandler:         enterprise.NewComputeStreamHandler,
		},
		enterprise.NewExecutorProxyHandler,
		enterprise.NewGitHubAppSetupHandler,
	)
	httpServer := &http.Server{
		Handler:      externalHandler,
		ReadTimeout:  75 * time.Second,
		WriteTimeout: 10 * time.Minute,
	}

	server := httpserver.New(listener, httpServer, makeServerOptions()...)
	logger.Debug("HTTP running", sglog.String("on", httpAddr))
	return server, nil
}

func makeInternalAPI(
	db database.DB,
	logger sglog.Logger,
	schema *graphql.Schema,
	enterprise enterprise.Services,
	rateLimiter graphqlbackend.LimitWatcher,
) (goroutine.BackgroundRoutine, error) {
	if httpAddrInternal == "" {
		return nil, nil
	}

	listener, err := httpserver.NewListener(httpAddrInternal)
	if err != nil {
		return nil, err
	}

	// The internal HTTP handler does not include the auth handlers.
	internalHandler := newInternalHTTPHandler(
		schema,
		db,
		enterprise.NewCodeIntelUploadHandler,
		enterprise.RankingService,
		enterprise.NewComputeStreamHandler,
		rateLimiter,
	)
	httpServer := &http.Server{
		Handler:     internalHandler,
		ReadTimeout: 75 * time.Second,
		// Higher since for internal RPCs which can have large responses
		// (eg git archive). Should match the timeout used for git archive
		// in gitserver.
		WriteTimeout: time.Hour,
	}

	server := httpserver.New(listener, httpServer, makeServerOptions()...)
	logger.Debug("HTTP (internal) running", sglog.String("on", httpAddrInternal))
	return server, nil
}

func makeServerOptions() (options []httpserver.ServerOptions) {
	if deploy.IsDeployTypeKubernetes(deploy.Type()) {
		// On kubernetes, we want to wait an additional 5 seconds after we receive a
		// shutdown request to give some additional time for the endpoint changes
		// to propagate to services talking to this server like the LB or ingress
		// controller. We only do this in frontend and not on all services, because
		// frontend is the only publicly exposed service where we don't control
		// retries on connection failures (see httpcli.InternalClient).
		options = append(options, httpserver.WithPreShutdownPause(time.Second*5))
	}

	return options
}

func isAllowedOrigin(origin string, allowedOrigins []string) bool {
	for _, o := range allowedOrigins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

func makeRateLimitWatcher() (*graphqlbackend.BasicLimitWatcher, error) {
	var store throttled.GCRAStore
	var err error
	if pool, ok := redispool.Cache.Pool(); ok {
		store, err = redigostore.New(pool, "gql:rl:", 0)
	} else {
		// If redis is disabled we are in Sourcegraph App and can rely on an
		// in-memory store.
		store, err = memstore.New(0)
	}
	if err != nil {
		return nil, err
	}

	return graphqlbackend.NewBasicLimitWatcher(sglog.Scoped("BasicLimitWatcher", "basic rate-limiter"), store), nil
}
