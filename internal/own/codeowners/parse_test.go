package codeowners_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sourcegraph/sourcegraph/internal/own/codeowners"

	codeownerspb "github.com/sourcegraph/sourcegraph/internal/own/codeowners/proto"
)

func TestParseGithubExample(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(
		`# This is a comment.
# Each line is a file pattern followed by one or more owners.

# These owners will be the default owners for everything in
# the repo. Unless a later match takes precedence,
# @global-owner1 and @global-owner2 will be requested for
# review when someone opens a pull request.
*       @global-owner1 @global-owner2

# Order is important; the last matching pattern takes the most
# precedence. When someone opens a pull request that only
# modifies JS files, only @js-owner and not the global
# owner(s) will be requested for a review.
*.js    @js-owner #This is an inline comment.

# You can also use email addresses if you prefer. They'll be
# used to look up users just like we do for commit author
# emails.
*.go docs@example.com

# Teams can be specified as code owners as well. Teams should
# be identified in the format @org/team-name. Teams must have
# explicit write access to the repository. In this example,
# the octocats team in the octo-org organization owns all .txt files.
*.txt @octo-org/octocats

# In this example, @doctocat owns any files in the build/logs
# directory at the root of the repository and any of its
# subdirectories.
/build/logs/ @doctocat

# The docs/* pattern will match files like
# docs/getting-started.md but not further nested files like
# docs/build-app/troubleshooting.md.
docs/*  docs@example.com

# In this example, @octocat owns any file in an apps directory
# anywhere in your repository.
apps/ @octocat

# In this example, @doctocat owns any file in the /docs
# directory in the root of your repository and any of its
# subdirectories.
/docs/ @doctocat

# In this example, any change inside the /scripts directory
# will require approval from @doctocat or @octocat.
/scripts/ @doctocat @octocat

# In this example, @octocat owns any file in the /apps
# directory in the root of your repository except for the /apps/github
# subdirectory, as its owners are left empty.
/apps/ @octocat
/apps/github`))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{
		{
			Pattern: "*",
			Owner: []*codeownerspb.Owner{
				{Handle: "global-owner1"},
				{Handle: "global-owner2"},
			},
		},
		{
			Pattern: "*.js",
			Owner: []*codeownerspb.Owner{
				{Handle: "js-owner"},
			},
		},
		{
			Pattern: "*.go",
			Owner: []*codeownerspb.Owner{
				{Email: "docs@example.com"},
			},
		},
		{
			Pattern: "*.txt",
			Owner: []*codeownerspb.Owner{
				{Handle: "octo-org/octocats"},
			},
		},
		{
			Pattern: "/build/logs/",
			Owner: []*codeownerspb.Owner{
				{Handle: "doctocat"},
			},
		},
		{
			Pattern: "docs/*",
			Owner: []*codeownerspb.Owner{
				{Email: "docs@example.com"},
			},
		},
		{
			Pattern: "apps/",
			Owner: []*codeownerspb.Owner{
				{Handle: "octocat"},
			},
		},
		{
			Pattern: "/docs/",
			Owner: []*codeownerspb.Owner{
				{Handle: "doctocat"},
			},
		},
		{
			Pattern: "/scripts/",
			Owner: []*codeownerspb.Owner{
				{Handle: "doctocat"},
				{Handle: "octocat"},
			},
		},
		{
			Pattern: "/apps/",
			Owner: []*codeownerspb.Owner{
				{Handle: "octocat"},
			},
		},
		{
			Pattern: "/apps/github",
			Owner:   nil,
		},
	}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseGitlabExample(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(
		`# This is an example of a CODEOWNERS file.
# Lines that start with '#' are ignored.

# app/ @commented-rule

# Specify a default Code Owner by using a wildcard:
* @default-codeowner

# Specify multiple Code Owners by using a tab or space:
* @multiple @code @owners

# Rules defined later in the file take precedence over the rules
# defined before.
# For example, for all files with a filename ending in '.rb':
*.rb @ruby-owner

# Files with a '#' can still be accessed by escaping the pound sign:
\#file_with_pound.rb @owner-file-with-pound

# Specify multiple Code Owners separated by spaces or tabs.
# In the following case the CODEOWNERS file from the root of the repo
# has 3 Code Owners (@multiple @code @owners):
CODEOWNERS @multiple @code @owners

# You can use both usernames or email addresses to match
# users. Everything else is ignored. For example, this code
# specifies the '@legal' and a user with email 'janedoe@gitlab.com' as the
# owner for the LICENSE file:
LICENSE @legal this_does_not_match janedoe@gitlab.com

# Use group names to match groups, and nested groups to specify
# them as owners for a file:
README @group @group/with-nested/subgroup

# End a path in a '/' to specify the Code Owners for every file
# nested in that directory, on any level:
/docs/ @all-docs

# End a path in '/*' to specify Code Owners for every file in
# a directory, but not nested deeper. This code matches
# 'docs/index.md' but not 'docs/projects/index.md':
/docs/* @root-docs

# Include '/**' to specify Code Owners for all subdirectories
# in a directory. This rule matches 'docs/projects/index.md' or
# 'docs/development/index.md'
/docs/**/*.md @root-docs

# This code makes matches a 'lib' directory nested anywhere in the repository:
lib/ @lib-owner

# This code match only a 'config' directory in the root of the repository:
/config/ @config-owner

# If the path contains spaces, escape them like this:
path\ with\ spaces/ @space-owner

# Code Owners section:
[Documentation]
ee/docs    @docs
docs       @docs

[Database]
README.md  @database
model/db   @database

# This section is combined with the previously defined [Documentation] section:
[DOCUMENTATION]
README.md  @docs
`))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{
		{
			Pattern: "*",
			Owner: []*codeownerspb.Owner{
				{Handle: "default-codeowner"},
			},
		},
		{
			Pattern: "*",
			Owner: []*codeownerspb.Owner{
				{Handle: "multiple"},
				{Handle: "code"},
				{Handle: "owners"},
			},
		},
		{
			Pattern: "*.rb",
			Owner: []*codeownerspb.Owner{
				{Handle: "ruby-owner"},
			},
		},
		{
			Pattern: "#file_with_pound.rb",
			Owner: []*codeownerspb.Owner{
				{Handle: "owner-file-with-pound"},
			},
		},
		{
			Pattern: "CODEOWNERS",
			Owner: []*codeownerspb.Owner{
				{Handle: "multiple"},
				{Handle: "code"},
				{Handle: "owners"},
			},
		},
		{
			Pattern: "LICENSE",
			Owner: []*codeownerspb.Owner{
				{Handle: "legal"},
				// Note: To match GitLab parsing, we should not consider this as email.
				{Email: "this_does_not_match"},
				{Email: "janedoe@gitlab.com"},
			},
		},
		{
			Pattern: "README",
			Owner: []*codeownerspb.Owner{
				{Handle: "group"},
				{Handle: "group/with-nested/subgroup"},
			},
		},
		{
			Pattern: "/docs/",
			Owner: []*codeownerspb.Owner{
				{Handle: "all-docs"},
			},
		},
		{
			Pattern: "/docs/*",
			Owner: []*codeownerspb.Owner{
				{Handle: "root-docs"},
			},
		},
		{
			Pattern: "/docs/**/*.md",
			Owner: []*codeownerspb.Owner{
				{Handle: "root-docs"},
			},
		},
		{
			Pattern: "lib/",
			Owner: []*codeownerspb.Owner{
				{Handle: "lib-owner"},
			},
		},
		{
			Pattern: "/config/",
			Owner: []*codeownerspb.Owner{
				{Handle: "config-owner"},
			},
		},
		{
			Pattern: "path with spaces/",
			Owner: []*codeownerspb.Owner{
				{Handle: "space-owner"},
			},
		},
		{
			Pattern: "ee/docs",
			Owner: []*codeownerspb.Owner{
				{Handle: "docs"},
			},
			SectionName: "documentation",
		},
		{
			Pattern: "docs",
			Owner: []*codeownerspb.Owner{
				{Handle: "docs"},
			},
			SectionName: "documentation",
		},
		{
			Pattern: "README.md",
			Owner: []*codeownerspb.Owner{
				{Handle: "database"},
			},
			SectionName: "database",
		},
		{
			Pattern: "model/db",
			Owner: []*codeownerspb.Owner{
				{Handle: "database"},
			},
			SectionName: "database",
		},
		{
			Pattern: "README.md",
			Owner: []*codeownerspb.Owner{
				{Handle: "docs"},
			},
			SectionName: "documentation",
		},
	}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseAtHandle(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader("README.md @readme-team"))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{{
		Pattern: "README.md",
		Owner: []*codeownerspb.Owner{
			{Handle: "readme-team"},
		},
	}}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseAtHandleSupportsNesting(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader("README.md @readme-team/readme-subteam"))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{{
		Pattern: "README.md",
		Owner: []*codeownerspb.Owner{
			{Handle: "readme-team/readme-subteam"},
		},
	}}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseEmailHandle(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader("README.md me@example.com"))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{{
		Pattern: "README.md",
		Owner: []*codeownerspb.Owner{
			{Email: "me@example.com"},
		},
	}}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseTwoHandles(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader("README.md @readme-team me@example.com"))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{{
		Pattern: "README.md",
		Owner: []*codeownerspb.Owner{
			{Handle: "readme-team"},
			{Email: "me@example.com"},
		},
	}}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParsePathWithSpaces(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(`path\ with\ spaces/* @space-owner`))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{{
		Pattern: "path with spaces/*",
		Owner: []*codeownerspb.Owner{
			{Handle: "space-owner"},
		},
	}}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseSection(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(
		`[PM]
		own/codeowners/* @own-pms`))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{{
		Pattern:     "own/codeowners/*",
		SectionName: "pm",
		Owner: []*codeownerspb.Owner{
			{Handle: "own-pms"},
		},
	}}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseManySections(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(
		`own/codeowners/* @own-eng
		[PM]
		own/codeowners/* @own-pms
		[docs]
		own/**/*.md @own-docs`))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{
		{
			Pattern: "own/codeowners/*",
			Owner: []*codeownerspb.Owner{
				{Handle: "own-eng"},
			},
		},
		{
			Pattern:     "own/codeowners/*",
			SectionName: "pm",
			Owner: []*codeownerspb.Owner{
				{Handle: "own-pms"},
			},
		},
		{
			Pattern:     "own/**/*.md",
			SectionName: "docs",
			Owner: []*codeownerspb.Owner{
				{Handle: "own-docs"},
			},
		},
	}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

func TestParseEmptyString(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, &codeownerspb.File{}, got)
}

func TestParseBlankString(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader("  "))
	require.NoError(t, err)
	assert.Equal(t, &codeownerspb.File{}, got)
}

func TestParseComment(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(" # This is a comment "))
	require.NoError(t, err)
	assert.Equal(t, &codeownerspb.File{}, got)
}

func TestParseRuleWithComment(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(`/escaped\#/is/pattern @and-then # Inline comment`))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{
		{
			Pattern: "/escaped#/is/pattern",
			Owner: []*codeownerspb.Owner{
				{Handle: "and-then"},
			},
		},
	}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}

// Note: Should a # within [Section name] not be treated as a comment-start
// even if it is not escaped?
func TestParseSectionWithComment(t *testing.T) {
	got, err := codeowners.Parse(strings.NewReader(
		`[Section] # Inline comment
		/pattern @owner`))
	require.NoError(t, err)
	want := []*codeownerspb.Rule{
		{
			Pattern:     "/pattern",
			SectionName: "section",
			Owner: []*codeownerspb.Owner{
				{Handle: "owner"},
			},
		},
	}
	assert.Equal(t, &codeownerspb.File{Rule: want}, got)
}
