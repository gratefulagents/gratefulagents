import { useState } from "react";
import { client } from "@/lib/client";
import { describeRpcError, isTransientRpcError } from "@/lib/rpc-errors";
import type { CreateProjectRequest, Project } from "@/rpc/platform/service_pb";

export function useCreateProject() {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function createProject(request: CreateProjectRequest): Promise<Project> {
    setSubmitting(true);
    setError(null);
    try {
      return await client.createProject(request);
    } catch (err) {
      // Mutations are deliberately not auto-retried (the server may have
      // already applied the change). Surface a clear, human message inline;
      // the dialog keeps all form state so the user can just hit Create again.
      let message = describeRpcError(err, "create the project");
      if (isTransientRpcError(err)) {
        message += " Your form is untouched — try Create again.";
      }
      setError(message);
      throw err;
    } finally {
      setSubmitting(false);
    }
  }

  return { createProject, submitting, error, clearError: () => setError(null) };
}
