package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const maxSecuritySubmissionBundleBytes = int64(2 << 20)

// GetSecurityFindingSubmissionBundle returns a private per-finding bounty ZIP.
// The object key is loaded from trusted finding-scoped metadata after the same
// namespace and scan visibility checks used by GetSecurityFinding; callers can
// never supply a bucket or object key.
func (s *Server) GetSecurityFindingSubmissionBundle(ctx context.Context, req *platform.GetSecurityFindingSubmissionBundleRequest) (*platform.GetSecurityFindingSubmissionBundleResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetFindingId(), req.GetNamespace(), "")
	if err != nil {
		return nil, err
	}
	artifacts, ok := sec.(store.SecurityFindingArtifactStore)
	if !ok {
		return &platform.GetSecurityFindingSubmissionBundleResponse{Status: "unavailable"}, nil
	}
	artifact, err := artifacts.GetSecurityFindingArtifact(ctx, finding.Namespace, finding.ID, finding.ExecutionID, store.SecurityFindingArtifactSubmissionBundle)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting submission bundle metadata: %w", err))
	}
	if artifact == nil {
		return &platform.GetSecurityFindingSubmissionBundleResponse{Status: "unavailable"}, nil
	}
	resp := &platform.GetSecurityFindingSubmissionBundleResponse{
		Status: artifact.Status, Error: artifact.Error, Filename: artifact.Filename,
		Sha256: artifact.SHA256, SizeBytes: artifact.SizeBytes,
	}
	if !artifact.CreatedAt.IsZero() {
		resp.CreatedAt = timestamppb.New(artifact.CreatedAt)
	}
	if artifact.Status != "ready" {
		if resp.Status == "" {
			resp.Status = "generating"
		}
		return resp, nil
	}
	if s.s3Reader == nil || s.s3Reader.client == nil {
		resp.Status, resp.Error = "error", "private artifact storage is unavailable"
		return resp, nil
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	prefix := "security-submissions/v1/" + finding.Namespace + "/"
	if bucket == "" || !strings.HasPrefix(artifact.S3Key, prefix) || strings.Contains(artifact.S3Key, "..") {
		resp.Status, resp.Error = "error", "stored bundle reference is invalid"
		return resp, nil
	}
	if artifact.SizeBytes <= 0 || artifact.SizeBytes > maxSecuritySubmissionBundleBytes {
		resp.Status, resp.Error = "error", "stored bundle size is invalid"
		return resp, nil
	}
	output, err := s.s3Reader.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(artifact.S3Key)})
	if err != nil {
		resp.Status, resp.Error = "error", "private bundle could not be fetched"
		return resp, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(output.Body, maxSecuritySubmissionBundleBytes+1))
	closeErr := output.Body.Close()
	if readErr != nil || closeErr != nil || int64(len(body)) > maxSecuritySubmissionBundleBytes || int64(len(body)) != artifact.SizeBytes {
		resp.Status, resp.Error = "error", "private bundle failed bounded read verification"
		return resp, nil
	}
	digest := sha256.Sum256(body)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), artifact.SHA256) {
		resp.Status, resp.Error = "error", "private bundle checksum verification failed"
		return resp, nil
	}
	resp.Content = body
	return resp, nil
}

func (h *PlatformServiceConnectHandler) GetSecurityFindingSubmissionBundle(ctx context.Context, req *connect.Request[platform.GetSecurityFindingSubmissionBundleRequest]) (*connect.Response[platform.GetSecurityFindingSubmissionBundleResponse], error) {
	resp, err := h.srv.GetSecurityFindingSubmissionBundle(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
