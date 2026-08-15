import { Navigate, useLocation, useParams } from "react-router-dom";

import { DetailErrorState } from "@/components/ui/detail-state";

/**
 * Redirect that carries the query string (and optional hash) to the new path.
 *
 * Plain `<Navigate to="/new" replace />` drops `?severity=critical`, which
 * silently breaks every bookmark and notification deep link that pointed at a
 * renamed route. Legacy security paths must keep their filters when they land
 * on the current route.
 */
export function RedirectPreservingQuery({
  to,
  /** Extra params merged into the destination query string. */
  params,
}: {
  to: string;
  params?: Record<string, string | undefined>;
}) {
  const location = useLocation();
  const routeParams = useParams();
  const search = new URLSearchParams(location.search);
  for (const [key, value] of Object.entries(params ?? {})) {
    // Values starting with ":" name a route param, e.g. { tab: ":tab" }.
    const resolved = value?.startsWith(":") ? routeParams[value.slice(1)] : value;
    if (resolved) search.set(key, resolved);
  }
  const query = search.toString();
  return <Navigate to={`${to}${query ? `?${query}` : ""}${location.hash}`} replace />;
}

/**
 * Terminal state for an unrecognised path under a section.
 *
 * Bouncing an unknown URL straight to the section root hides typos and dead
 * links; showing where the user landed plus the real destinations is both
 * honest and faster to recover from.
 */
export function SectionNotFound({
  section,
  links,
}: {
  section: string;
  links: Array<{ to: string; label: string }>;
}) {
  const location = useLocation();
  return (
    <DetailErrorState
      kind="not-found"
      title={`This ${section} page doesn't exist`}
      description="The link may be outdated or mistyped. Pick a destination below."
      detail={location.pathname}
      links={links}
    />
  );
}
