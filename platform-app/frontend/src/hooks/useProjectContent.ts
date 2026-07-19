import { useCallback, useEffect, useState } from "react";

import { binaryClient, client } from "@/lib/client";
import type { ProjectContent, ProjectContentVersion } from "@/rpc/platform/service_pb";

export interface UploadProgress {
  completed: number;
  total: number;
  current: string;
}

export type ContentFile = File;

const DIRECT_UPLOAD_LIMIT = 25 * 1024 * 1024;
const acceptedExtensions = new Set([
  "pdf", "docx", "xlsx", "pptx", "csv", "json", "txt", "md", "markdown",
  "png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "bmp", "tif", "tiff", "heic", "heif", "mp3", "wav", "m4a", "mp4",
  "mov", "webm", "zip", "tar", "gz", "tgz", "html", "htm",
]);

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function filePath(file: ContentFile): string {
  return file.webkitRelativePath || file.name;
}

function supportedFile(file: ContentFile): boolean {
  return acceptedExtensions.has(file.name.split(".").pop()?.toLowerCase() ?? "");
}

export function useProjectContent(namespace: string, projectName: string) {
  const [items, setItems] = useState<ProjectContent[]>([]);
  const [loading, setLoading] = useState(Boolean(namespace && projectName));
  const [error, setError] = useState<string | null>(null);
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(null);

  const refresh = useCallback(async () => {
    if (!namespace || !projectName) {
      setItems([]);
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    try {
      const response = await client.listProjectContent({ namespace, projectName, includeDeleted: false });
      setItems(response.items);
    } catch (cause) {
      setError(errorMessage(cause));
      throw cause;
    } finally {
      setLoading(false);
    }
  }, [namespace, projectName]);

  useEffect(() => {
    let active = true;
    // Initial synchronization with the durable project content service.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void refresh().catch(() => {
      if (!active) return;
    });
    return () => {
      active = false;
    };
  }, [refresh]);

  const mutate = useCallback(async <T,>(operation: () => Promise<T>): Promise<T> => {
    setError(null);
    try {
      const result = await operation();
      await refresh();
      return result;
    } catch (cause) {
      setError(errorMessage(cause));
      throw cause;
    }
  }, [refresh]);

  const create = useCallback((input: {
    kind: string;
    path: string;
    mediaType?: string;
    content?: Uint8Array;
  }) => mutate(() => binaryClient.createProjectContent({
    namespace,
    projectName,
    kind: input.kind,
    path: input.path,
    mediaType: input.mediaType ?? "",
    content: input.content ?? new Uint8Array(),
    metadataJson: "{}",
    provenanceJson: "{}",
  })), [mutate, namespace, projectName]);

  const upload = useCallback(async (files: FileList | ContentFile[]) => {
    const selected = Array.from(files as ArrayLike<ContentFile>);
    const rejected = selected.filter((file) => file.size > DIRECT_UPLOAD_LIMIT || !supportedFile(file));
    const valid = selected.filter((file) => !rejected.includes(file));
    if (rejected.length) {
      const descriptions = rejected.map((file) =>
        file.size > DIRECT_UPLOAD_LIMIT
          ? `${filePath(file)} exceeds the 25 MiB direct-upload limit`
          : `${filePath(file)} is not an accepted file type`,
      );
      setError(descriptions.join(". "));
    }
    if (!valid.length) return;

    setUploadProgress({ completed: 0, total: valid.length, current: filePath(valid[0]) });
    setError(null);
    try {
      for (let index = 0; index < valid.length; index += 1) {
        const file = valid[index];
        setUploadProgress({ completed: index, total: valid.length, current: filePath(file) });
        const bytes = new Uint8Array(await file.arrayBuffer());
        try {
          await binaryClient.createProjectContent({
            namespace,
            projectName,
            kind: "file",
            path: filePath(file),
            mediaType: file.type,
            content: bytes,
            metadataJson: "{}",
            provenanceJson: "{}",
          });
        } catch (cause) {
          setError(errorMessage(cause));
          throw cause;
        }
        setUploadProgress({ completed: index + 1, total: valid.length, current: filePath(file) });
      }
    } finally {
      setUploadProgress(null);
      // Refresh once after the batch instead of after every file.
      await refresh().catch(() => undefined);
    }
  }, [namespace, projectName, refresh]);

  const get = useCallback(async (id: string, version = 0) => {
    setError(null);
    try {
      return await binaryClient.getProjectContent({ namespace, projectName, id, version });
    } catch (cause) {
      setError(errorMessage(cause));
      throw cause;
    }
  }, [namespace, projectName]);

  const update = useCallback((input: {
    item: ProjectContent;
    path?: string;
    mediaType?: string;
    content?: Uint8Array;
    confirmOverwrite?: boolean;
  }) => mutate(() => binaryClient.updateProjectContent({
    namespace,
    projectName,
    id: input.item.id,
    path: input.path ?? "",
    mediaType: input.mediaType ?? "",
    content: input.content,
    metadataJson: "",
    provenanceJson: "",
    expectedVersion: input.item.currentVersion,
    confirmOverwrite: input.confirmOverwrite ?? false,
  })), [mutate, namespace, projectName]);

  const duplicate = useCallback((item: ProjectContent, destinationPath: string) =>
    mutate(() => client.duplicateProjectContent({ namespace, projectName, id: item.id, destinationPath })),
  [mutate, namespace, projectName]);

  const listVersions = useCallback(async (item: ProjectContent): Promise<ProjectContentVersion[]> => {
    setError(null);
    try {
      const response = await client.listProjectContentVersions({ namespace, projectName, id: item.id });
      return response.versions;
    } catch (cause) {
      setError(errorMessage(cause));
      throw cause;
    }
  }, [namespace, projectName]);

  const restore = useCallback((item: ProjectContent, version: number) =>
    mutate(() => client.restoreProjectContentVersion({
      namespace,
      projectName,
      id: item.id,
      version,
      expectedVersion: item.currentVersion,
    })), [mutate, namespace, projectName]);

  const remove = useCallback((item: ProjectContent) =>
    mutate(() => client.deleteProjectContent({ namespace, projectName, id: item.id, confirm: true })),
  [mutate, namespace, projectName]);

  return {
    items,
    loading,
    error,
    uploadProgress,
    refresh,
    create,
    upload,
    get,
    update,
    duplicate,
    listVersions,
    restore,
    remove,
  };
}

export const projectContentAccept = ".pdf,.docx,.xlsx,.pptx,.csv,.json,.txt,.md,.markdown,.png,.jpg,.jpeg,.gif,.webp,.svg,.avif,.bmp,.tif,.tiff,.heic,.heif,.mp3,.wav,.m4a,.mp4,.mov,.webm,.zip,.tar,.gz,.tgz,.html,.htm";
