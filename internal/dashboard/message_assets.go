package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

// persistMessageImageAssets adds images attached to a project-backed run to
// that project's durable Files & artifacts collection. Standalone runs retain
// ordinary multimodal message attachments without creating project assets.
func (s *Server) persistMessageImageAssets(ctx context.Context, run *platformv1alpha1.AgentRun, images []sessionclient.MessageImage) ([]sessionclient.MessageImage, error) {
	projectName := agentRunProjectName(run)
	if projectName == "" || len(images) == 0 {
		return images, nil
	}
	if err := s.requireProjectContentAccess(ctx, run.Namespace, projectName, AccessCollaborator, "attach images to this project"); err != nil {
		return nil, err
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		return nil, err
	}

	result := append([]sessionclient.MessageImage(nil), images...)
	created := make([]store.ProjectContent, 0, len(images))
	actor := requestActorFromContext(ctx).Subject
	cleanup := func() {
		for i := len(created) - 1; i >= 0; i-- {
			item := created[i]
			_ = contentStore.SoftDeleteContent(context.Background(), item.ID, store.SoftDeleteContentOptions{
				ExpectedVersion: item.CurrentVersion,
				Confirmed:       true,
				Actor:           actor,
			})
		}
	}

	for i, image := range result {
		body, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(image.Data))
		if decodeErr != nil {
			cleanup()
			return nil, fmt.Errorf("decoding image attachment %d for project asset: %w", i+1, decodeErr)
		}
		assetPath := path.Join("chat-attachments", run.Name, uuid.NewString()+imageAssetExtension(image.MediaType))
		if validateErr := validateContentBytes(store.ProjectContentKindFile, assetPath, body); validateErr != nil {
			cleanup()
			return nil, validateErr
		}
		metadata, _ := json.Marshal(map[string]any{
			"source":        "chat-attachment",
			"agent_run":     run.Name,
			"message_index": i,
		})
		provenance, _ := json.Marshal(map[string]any{
			"kind":      "pasted-image",
			"agent_run": run.Name,
		})
		item, createErr := contentStore.CreateContent(ctx, store.CreateContentOptions{
			ProjectNamespace: run.Namespace,
			ProjectName:      projectName,
			Kind:             store.ProjectContentKindFile,
			Path:             assetPath,
			MediaType:        image.MediaType,
			Content:          body,
			Metadata:         metadata,
			Provenance:       provenance,
			ScanStatus:       store.ScanStatusClean,
			Actor:            actor,
		})
		if createErr != nil {
			cleanup()
			return nil, fmt.Errorf("creating project asset for image attachment %d: %w", i+1, createErr)
		}
		created = append(created, *item)
		result[i].AssetID = item.ID.String()
		result[i].AssetVersion = item.CurrentVersion
		result[i].AssetSHA256 = item.ContentHash
		result[i].AssetPath = item.Path
		result[i].ProjectName = projectName
	}
	return result, nil
}

func (s *Server) deleteMessageImageAssets(ctx context.Context, images []sessionclient.MessageImage) {
	hasAssetReference := false
	for _, image := range images {
		if image.AssetID != "" {
			hasAssetReference = true
			break
		}
	}
	if !hasAssetReference {
		return
	}
	contentStore, err := s.projectContentStore()
	if err != nil {
		log.Printf("WARN: project asset cleanup unavailable: %v", err)
		return
	}
	actor := requestActorFromContext(ctx).Subject
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, image := range images {
		id, parseErr := uuid.Parse(image.AssetID)
		if parseErr != nil {
			continue
		}
		item, getErr := contentStore.GetContent(cleanupCtx, id)
		if getErr != nil {
			log.Printf("WARN: loading generated project asset %s for cleanup: %v", id, getErr)
			continue
		}
		if item.Path != image.AssetPath || item.ProjectName != image.ProjectName {
			log.Printf("WARN: refusing cleanup for mismatched generated project asset %s", id)
			continue
		}
		if deleteErr := contentStore.SoftDeleteContent(cleanupCtx, id, store.SoftDeleteContentOptions{
			ExpectedVersion: item.CurrentVersion,
			Confirmed:       true,
			Actor:           actor,
		}); deleteErr != nil {
			log.Printf("WARN: deleting generated project asset %s during message cleanup: %v", id, deleteErr)
		}
	}
}

func messageAssetError(err error) error {
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("storing image attachments as project assets: %w", err))
}

func imageAssetExtension(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0])) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/avif":
		return ".avif"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	case "image/heic", "image/heif":
		return ".heic"
	default:
		return ".png"
	}
}
