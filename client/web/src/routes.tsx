import * as React from 'react'

import { Redirect, RouteComponentProps } from 'react-router'

import { ThemeProps } from '@sourcegraph/shared/src/theme'
import { lazyComponent } from '@sourcegraph/shared/src/util/lazyComponent'

import { BatchChangesProps } from './batches'
import { CodeIntelligenceProps } from './codeintel'
import { communitySearchContextsRoutes } from './communitySearchContexts/routes'
import { BreadcrumbsProps, BreadcrumbSetters } from './components/Breadcrumbs'
import type { LayoutProps } from './Layout'
import { PageRoutes } from './routes.constants'
import { SearchPageWrapper } from './search/SearchPageWrapper'
import { getExperimentalFeatures } from './stores'
import { ThemePreferenceProps } from './theme'

const SiteAdminArea = lazyComponent(() => import('./site-admin/SiteAdminArea'), 'SiteAdminArea')
const SearchConsolePage = lazyComponent(() => import('./search/SearchConsolePage'), 'SearchConsolePage')
const SignInPage = lazyComponent(() => import('./auth/SignInPage'), 'SignInPage')
const SignUpPage = lazyComponent(() => import('./auth/SignUpPage'), 'SignUpPage')
const UnlockAccountPage = lazyComponent(() => import('./auth/UnlockAccount'), 'UnlockAccountPage')
const SiteInitPage = lazyComponent(() => import('./site-admin/init/SiteInitPage'), 'SiteInitPage')
const InstallGitHubAppSuccessPage = lazyComponent(
    () => import('./org/settings/codeHosts/InstallGitHubAppSuccessPage'),
    'InstallGitHubAppSuccessPage'
)

export interface LayoutRouteComponentProps<RouteParameters extends { [K in keyof RouteParameters]?: string }>
    extends RouteComponentProps<RouteParameters>,
        Omit<LayoutProps, 'match'>,
        ThemeProps,
        ThemePreferenceProps,
        BreadcrumbsProps,
        BreadcrumbSetters,
        CodeIntelligenceProps,
        BatchChangesProps {
    isSourcegraphDotCom: boolean
    isMacPlatform: boolean
}

export interface LayoutRouteProps<Parameters_ extends { [K in keyof Parameters_]?: string }> {
    path: string
    exact?: boolean
    render: (props: LayoutRouteComponentProps<Parameters_>) => React.ReactNode

    /**
     * A condition function that needs to return true if the route should be rendered
     *
     * @default () => true
     */
    condition?: (props: LayoutRouteComponentProps<Parameters_>) => boolean
}

// Force a hard reload so that we delegate to the serverside HTTP handler for a route.
function passThroughToServer(): React.ReactNode {
    window.location.reload()
    return null
}

/**
 * Holds all top-level routes for the app because both the navbar and the main content area need to
 * switch over matched path.
 *
 * See https://reacttraining.com/react-router/web/example/sidebar
 */
export const routes: readonly LayoutRouteProps<any>[] = (
    [
        {
            path: PageRoutes.Index,
            render: () => <Redirect to={PageRoutes.Search} />,
            exact: true,
        },
        {
            path: PageRoutes.Search,
            render: props => <SearchPageWrapper {...props} />,
            exact: true,
        },
        {
            path: PageRoutes.SearchConsole,
            render: props => {
                const { showMultilineSearchConsole } = getExperimentalFeatures()

                return showMultilineSearchConsole ? (
                    <SearchConsolePage {...props} />
                ) : (
                    <Redirect to={PageRoutes.Search} />
                )
            },
            exact: true,
        },
        {
            path: PageRoutes.SignIn,
            render: props => <SignInPage {...props} context={window.context} />,
            exact: true,
        },
        {
            path: PageRoutes.SignUp,
            render: props => <SignUpPage {...props} context={window.context} />,
            exact: true,
        },
        {
            path: PageRoutes.UnlockAccount,
            render: props => <UnlockAccountPage {...props} context={window.context} />,
            exact: true,
        },
        {
            path: PageRoutes.Welcome,
            // This route is deprecated after we removed the post-sign-up page experimental feature, but we keep it for now to not break links.
            render: props => <Redirect to={PageRoutes.Search} />,
            exact: true,
        },
        {
            path: PageRoutes.InstallGitHubAppSuccess,
            render: () => <InstallGitHubAppSuccessPage />,
        },
        {
            path: PageRoutes.Settings,
            render: lazyComponent(() => import('./user/settings/RedirectToUserSettings'), 'RedirectToUserSettings'),
        },
        {
            path: PageRoutes.User,
            render: lazyComponent(() => import('./user/settings/RedirectToUserPage'), 'RedirectToUserPage'),
        },
        {
            path: PageRoutes.Organizations,
            render: lazyComponent(() => import('./org/OrgsArea'), 'OrgsArea'),
        },
        {
            path: PageRoutes.SiteAdminInit,
            exact: true,
            render: props => <SiteInitPage {...props} context={window.context} />,
        },
        {
            path: PageRoutes.SiteAdmin,
            render: props => (
                <SiteAdminArea
                    {...props}
                    routes={props.siteAdminAreaRoutes}
                    sideBarGroups={props.siteAdminSideBarGroups}
                    overviewComponents={props.siteAdminOverviewComponents}
                />
            ),
        },
        {
            path: PageRoutes.PasswordReset,
            render: lazyComponent(() => import('./auth/ResetPasswordPage'), 'ResetPasswordPage'),
            exact: true,
        },
        {
            path: PageRoutes.ApiConsole,
            render: lazyComponent(() => import('./api/ApiConsole'), 'ApiConsole'),
            exact: true,
        },
        {
            path: PageRoutes.UserArea,
            render: lazyComponent(() => import('./user/area/UserArea'), 'UserArea'),
        },
        {
            path: PageRoutes.Survey,
            render: lazyComponent(() => import('./marketing/page/SurveyPage'), 'SurveyPage'),
        },
        {
            path: PageRoutes.Help,
            render: passThroughToServer,
        },
        {
            path: PageRoutes.Debug,
            render: passThroughToServer,
        },
        ...communitySearchContextsRoutes,
        {
            path: PageRoutes.RepoContainer,
            render: lazyComponent(() => import('./repo/RepoContainer'), 'RepoContainer'),
        },
    ] as readonly (LayoutRouteProps<any> | undefined)[]
).filter(Boolean) as readonly LayoutRouteProps<any>[]
