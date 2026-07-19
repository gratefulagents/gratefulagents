import { useEffect, useRef } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { useAuth } from "@/contexts/AuthContext";
import { useMyCredentials } from "@/hooks/useMyCredentials";
import { useProjects } from "@/hooks/useWatchedList";
import { onboardingDismissed, shouldOfferOnboarding } from "@/lib/onboarding";

/**
 * OnboardingRedirect sends brand-new users (no credentials, no projects) from
 * the Home screen to the /welcome wizard, once per session. It never hijacks
 * deep links, viewers, or anyone who already skipped setup on this device.
 */
export function OnboardingRedirect() {
  const { user } = useAuth();
  const { projects, loading: projectsLoading } = useProjects();
  const { presence, loading: credsLoading } = useMyCredentials();
  const location = useLocation();
  const navigate = useNavigate();
  const decided = useRef(false);

  useEffect(() => {
    if (decided.current || projectsLoading || credsLoading || !presence) return;
    // Decide exactly once per shell mount, and only from the Home screen so a
    // shared run/project link is never interrupted.
    decided.current = true;
    if (location.pathname !== "/") return;
    const eligible = shouldOfferOnboarding({
      presence,
      projectCount: projects.length,
      role: user?.role,
      dismissed: onboardingDismissed(user?.id),
    });
    if (eligible) navigate("/welcome", { replace: true });
  }, [projectsLoading, credsLoading, presence, projects.length, user, location.pathname, navigate]);

  return null;
}
