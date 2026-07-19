package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"path"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const maxProjectContentBytes = store.MaxProjectContentVersionBytes

// MaxRPCReadBytes is the transport-level read limit for the platform RPC
// handler. It must exceed maxProjectContentBytes with headroom for protobuf
// framing and the request's other fields, or uploads in the advertised
// 4-25 MiB range would be rejected by Connect's default limit before
// validation runs.
const MaxRPCReadBytes = 32 << 20 // 32 MiB

var allowedProjectContentExtensions = map[string]bool{
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true,
	".csv": true, ".json": true, ".txt": true, ".md": true, ".markdown": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
	".avif": true, ".bmp": true, ".tif": true, ".tiff": true, ".heic": true, ".heif": true,
	".mp3": true, ".wav": true, ".m4a": true, ".mp4": true, ".mov": true, ".webm": true,
	".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".html": true, ".htm": true,
}

// EICAR is the standard harmless anti-malware test signature. This local
// scanner blocks the canonical signature immediately; production deployments
// can add asynchronous scanners before changing scan_status from clean.
var eicarSignature = []byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*")

func (s *Server) projectContentStore() (store.ProjectContentStore, error) {
	contentStore, ok := s.stateStore.(store.ProjectContentStore)
	if !ok || contentStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("durable project content requires the Postgres state store"))
	}
	return contentStore, nil
}

func (s *Server) ListProjectContent(ctx context.Context, req *platform.ListProjectContentRequest) (*platform.ListProjectContentResponse, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessViewer, "view project files"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	items, err := contentStore.ListContent(ctx, req.Namespace, req.ProjectName, req.IncludeDeleted)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if req.PageSize > 0 && int(req.PageSize) < len(items) {
		items = items[:req.PageSize]
	}
	response := &platform.ListProjectContentResponse{Items: make([]*platform.ProjectContent, 0, len(items))}
	for i := range items {
		response.Items = append(response.Items, projectContentProto(&items[i]))
	}
	return response, nil
}

func (s *Server) GetProjectContent(ctx context.Context, req *platform.GetProjectContentRequest) (*platform.GetProjectContentResponse, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessViewer, "read this project file"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	id, err := parseContentID(req.Id)
	if err != nil {
		return nil, err
	}
	item, err := contentStore.GetContent(ctx, id)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if err := contentBelongsTo(item, req.Namespace, req.ProjectName); err != nil {
		return nil, err
	}
	version, err := contentStore.GetContentVersion(ctx, id, int(req.Version))
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	actor := requestActorFromContext(ctx).Subject
	// Best effort: an audit-trail hiccup must never block reads.
	if err := contentStore.RecordContentAudit(ctx, id, "download", actor, json.RawMessage(fmt.Sprintf(`{"version":%d}`, version.Version))); err != nil {
		log.Printf("WARN: recording project content download audit for %s: %v", id, err)
	}
	return &platform.GetProjectContentResponse{Item: projectContentProto(item), Content: version.Content, Version: int32(version.Version)}, nil
}

func (s *Server) CreateProjectContent(ctx context.Context, req *platform.CreateProjectContentRequest) (*platform.ProjectContent, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessCollaborator, "upload project files"); err != nil {
		return nil, err
	}
	kind, cleanPath, mediaType, metadata, provenance, err := validateProjectContent(req.Kind, req.Path, req.MediaType, req.Content, req.MetadataJson, req.ProvenanceJson)
	if err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	item, err := contentStore.CreateContent(ctx, store.CreateContentOptions{ProjectNamespace: req.Namespace, ProjectName: req.ProjectName, Kind: kind, Path: cleanPath, MediaType: mediaType, Content: req.Content, Metadata: metadata, Provenance: provenance, ScanStatus: store.ScanStatusClean, Actor: requestActorFromContext(ctx).Subject})
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	return projectContentProto(item), nil
}

func (s *Server) UpdateProjectContent(ctx context.Context, req *platform.UpdateProjectContentRequest) (*platform.ProjectContent, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessCollaborator, "edit this project file"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	id, err := parseContentID(req.Id)
	if err != nil {
		return nil, err
	}
	current, err := contentStore.GetContent(ctx, id)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if err := contentBelongsTo(current, req.Namespace, req.ProjectName); err != nil {
		return nil, err
	}
	opts := store.UpdateContentOptions{ExpectedVersion: int(req.ExpectedVersion), OverwriteConfirmed: req.ConfirmOverwrite, Actor: requestActorFromContext(ctx).Subject}
	effectivePath := current.Path
	if req.Path != "" {
		cleanPath, err := cleanContentPath(req.Path)
		if err != nil {
			return nil, err
		}
		if err := validateContentBytes(current.Kind, cleanPath, nil); err != nil {
			return nil, err
		}
		opts.Path = &cleanPath
		effectivePath = cleanPath
	}
	if req.MediaType != "" {
		mediaType := normalizeMediaType(req.MediaType, effectivePath)
		opts.MediaType = &mediaType
	}
	if req.Content != nil {
		if err := validateContentBytes(current.Kind, effectivePath, req.Content); err != nil {
			return nil, err
		}
		content := append([]byte(nil), req.Content...)
		opts.Content = &content
		scan := store.ScanStatusClean
		opts.ScanStatus = &scan
	}
	if req.MetadataJson != "" {
		value, err := validateJSONObject(req.MetadataJson, "metadata_json")
		if err != nil {
			return nil, err
		}
		opts.Metadata = &value
	}
	if req.ProvenanceJson != "" {
		value, err := validateJSONObject(req.ProvenanceJson, "provenance_json")
		if err != nil {
			return nil, err
		}
		opts.Provenance = &value
	}
	item, err := contentStore.UpdateContent(ctx, id, opts)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	return projectContentProto(item), nil
}

func (s *Server) DuplicateProjectContent(ctx context.Context, req *platform.DuplicateProjectContentRequest) (*platform.ProjectContent, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessCollaborator, "duplicate this project file"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	id, err := parseContentID(req.Id)
	if err != nil {
		return nil, err
	}
	current, err := contentStore.GetContent(ctx, id)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if err := contentBelongsTo(current, req.Namespace, req.ProjectName); err != nil {
		return nil, err
	}
	destination, err := cleanContentPath(req.DestinationPath)
	if err != nil {
		return nil, err
	}
	if err := validateContentBytes(current.Kind, destination, nil); err != nil {
		return nil, err
	}
	item, err := contentStore.DuplicateContent(ctx, id, store.DuplicateContentOptions{ProjectNamespace: req.Namespace, ProjectName: req.ProjectName, Path: destination, ExpectedVersion: current.CurrentVersion, Actor: requestActorFromContext(ctx).Subject})
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	return projectContentProto(item), nil
}

func (s *Server) ListProjectContentVersions(ctx context.Context, req *platform.ListProjectContentVersionsRequest) (*platform.ListProjectContentVersionsResponse, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessViewer, "view file history"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	id, err := parseContentID(req.Id)
	if err != nil {
		return nil, err
	}
	item, err := contentStore.GetContent(ctx, id)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if err := contentBelongsTo(item, req.Namespace, req.ProjectName); err != nil {
		return nil, err
	}
	versions, err := contentStore.ListContentVersions(ctx, id)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if req.PageSize > 0 && int(req.PageSize) < len(versions) {
		versions = versions[:req.PageSize]
	}
	response := &platform.ListProjectContentVersionsResponse{Versions: make([]*platform.ProjectContentVersion, 0, len(versions))}
	for _, version := range versions {
		response.Versions = append(response.Versions, projectContentVersionProto(&version))
	}
	return response, nil
}

func (s *Server) RestoreProjectContentVersion(ctx context.Context, req *platform.RestoreProjectContentVersionRequest) (*platform.ProjectContent, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessCollaborator, "restore this file version"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	id, err := parseContentID(req.Id)
	if err != nil {
		return nil, err
	}
	current, err := contentStore.GetContent(ctx, id)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if err := contentBelongsTo(current, req.Namespace, req.ProjectName); err != nil {
		return nil, err
	}
	item, err := contentStore.RestoreContent(ctx, id, int(req.Version), store.RestoreContentOptions{ExpectedVersion: int(req.ExpectedVersion), Actor: requestActorFromContext(ctx).Subject})
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	return projectContentProto(item), nil
}

func (s *Server) DeleteProjectContent(ctx context.Context, req *platform.DeleteProjectContentRequest) (*emptypb.Empty, error) {
	if err := s.requireProjectContentAccess(ctx, req.Namespace, req.ProjectName, AccessCollaborator, "delete this project file"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}
	id, err := parseContentID(req.Id)
	if err != nil {
		return nil, err
	}
	current, err := contentStore.GetContent(ctx, id)
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	if err := contentBelongsTo(current, req.Namespace, req.ProjectName); err != nil {
		return nil, err
	}
	err = contentStore.SoftDeleteContent(ctx, id, store.SoftDeleteContentOptions{ExpectedVersion: current.CurrentVersion, Confirmed: req.Confirm, Actor: requestActorFromContext(ctx).Subject})
	if err != nil {
		return nil, mapProjectContentError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) requireProjectContentAccess(ctx context.Context, namespace, projectName string, level ResourceAccessLevel, action string) error {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(projectName) == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and project_name are required"))
	}
	// Content rows carry no foreign key to the Project custom resource, so
	// verify the Project actually exists before consulting the ACL. Without
	// this, callers could pre-seed content (and consume storage quota) under
	// arbitrary namespace/name pairs that have no ownership records yet, and
	// that planted content would surface if the Project were later created.
	project := &triggersv1alpha1.Project{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: projectName}, project); err != nil {
		return mapK8sError(fmt.Sprintf("get Project %s/%s", namespace, projectName), err)
	}
	return s.requireResourceAccess(ctx, projectResourceType, projectName, namespace, level, action)
}

func validateProjectContent(kindValue, pathValue, mediaType string, content []byte, metadataValue, provenanceValue string) (store.ProjectContentKind, string, string, json.RawMessage, json.RawMessage, error) {
	kind := store.ProjectContentKind(strings.ToLower(strings.TrimSpace(kindValue)))
	switch kind {
	case store.ProjectContentKindFile, store.ProjectContentKindFolder, store.ProjectContentKindDocument, store.ProjectContentKindWorkbook, store.ProjectContentKindPresentation, store.ProjectContentKindHTML:
	default:
		return "", "", "", nil, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported content kind %q", kindValue))
	}
	cleanPath, err := cleanContentPath(pathValue)
	if err != nil {
		return "", "", "", nil, nil, err
	}
	if err := validateContentBytes(kind, cleanPath, content); err != nil {
		return "", "", "", nil, nil, err
	}
	metadata, err := validateJSONObject(metadataValue, "metadata_json")
	if err != nil {
		return "", "", "", nil, nil, err
	}
	provenance, err := validateJSONObject(provenanceValue, "provenance_json")
	if err != nil {
		return "", "", "", nil, nil, err
	}
	return kind, cleanPath, normalizeMediaType(mediaType, cleanPath), metadata, provenance, nil
}

func validateContentBytes(kind store.ProjectContentKind, filePath string, content []byte) error {
	if len(content) > maxProjectContentBytes {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("file exceeds the 25 MiB direct-upload limit"))
	}
	if kind == store.ProjectContentKindFolder && len(content) != 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("folders cannot contain file bytes"))
	}
	if kind != store.ProjectContentKindFolder && !allowedProjectContentExtensions[strings.ToLower(path.Ext(filePath))] {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("file type %q is not allowed by project policy", path.Ext(filePath)))
	}
	if bytes.Contains(content, eicarSignature) {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("upload rejected by malware scanning"))
	}
	return nil
}

func cleanContentPath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	invalid := value == "" || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') || len(value) > 1024
	for _, segment := range strings.Split(value, "/") {
		invalid = invalid || segment == ".."
	}
	cleaned := path.Clean(value)
	if invalid || cleaned == "." {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a valid project-relative path is required"))
	}
	return cleaned, nil
}

func normalizeMediaType(value, filePath string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		if parsed, _, err := mime.ParseMediaType(value); err == nil {
			return parsed
		}
	}
	if inferred := mime.TypeByExtension(strings.ToLower(path.Ext(filePath))); inferred != "" {
		if parsed, _, err := mime.ParseMediaType(inferred); err == nil {
			return parsed
		}
	}
	return "application/octet-stream"
}

func validateJSONObject(value, field string) (json.RawMessage, error) {
	if strings.TrimSpace(value) == "" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s must be a JSON object", field))
	}
	return json.RawMessage(value), nil
}

func parseContentID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid content id"))
	}
	return id, nil
}
func contentBelongsTo(item *store.ProjectContent, namespace, projectName string) error {
	if item.ProjectNamespace != namespace || item.ProjectName != projectName {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project content not found"))
	}
	return nil
}

func projectContentProto(item *store.ProjectContent) *platform.ProjectContent {
	pb := &platform.ProjectContent{Id: item.ID.String(), ProjectNamespace: item.ProjectNamespace, ProjectName: item.ProjectName, Kind: string(item.Kind), Path: item.Path, MediaType: item.MediaType, CurrentVersion: int32(item.CurrentVersion), SizeBytes: item.SizeBytes, ContentHash: item.ContentHash, CreatedBy: item.CreatedBy, ScanStatus: item.ScanStatus, MetadataJson: string(item.Metadata), ProvenanceJson: string(item.Provenance), CreatedAtUnix: item.CreatedAt.Unix(), UpdatedAtUnix: item.UpdatedAt.Unix()}
	if item.DeletedAt != nil {
		pb.DeletedAtUnix = item.DeletedAt.Unix()
	}
	return pb
}
func projectContentVersionProto(version *store.ProjectContentVersion) *platform.ProjectContentVersion {
	return &platform.ProjectContentVersion{Version: int32(version.Version), SizeBytes: version.Size, ContentHash: version.SHA256, CreatedBy: version.Creator, CreatedAtUnix: version.CreatedAt.Unix(), MetadataJson: string(version.Metadata)}
}

func mapProjectContentError(err error) error {
	switch {
	case errors.Is(err, store.ErrContentNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrContentConflict):
		return connect.NewError(connect.CodeAborted, fmt.Errorf("the file changed or its path is already in use; refresh and try again"))
	case errors.Is(err, store.ErrContentConfirmationRequired):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, store.ErrContentQuotaExceeded):
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("the project storage quota (%d MiB) is exhausted; delete unused files or versions", store.MaxProjectContentTotalBytes>>20))
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
