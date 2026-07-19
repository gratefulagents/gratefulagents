package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	// userNamespaceLabel marks namespaces auto-provisioned for a user so they can
	// be identified and cleaned up.
	userNamespaceLabel = "platform.gratefulagents.dev/user-namespace"
	// maxDNSLabelLen is the Kubernetes namespace (DNS-1123 label) length limit.
	maxDNSLabelLen = 63
	// userNamespaceHashLen is the length of the stability/uniqueness suffix
	// appended to the human-readable firstname-lastname portion.
	userNamespaceHashLen = 3
	// userNamespaceFallback is used when a display name yields no usable label.
	userNamespaceFallback = "user"
)

var nonDNSLabelChars = regexp.MustCompile(`[^a-z0-9]+`)

// deriveUserNamespaceName builds a stable, DNS-1123-safe namespace name of the
// form "<firstname>-<lastname>-<hash>" from a user's display name, falling back
// to "user" when no usable name is available. The 3-char hash is derived from the
// user's opaque subject so the same user always maps to the same namespace and
// distinct subjects with identical names do not collide.
func deriveUserNamespaceName(name, subject string) string {
	label := sanitizeDNSLabel(humanNameToLabel(name))
	if label == "" {
		label = userNamespaceFallback
	}
	hash := subjectHash(subject, userNamespaceHashLen)
	// Reserve room for "-" + hash within the DNS label limit.
	maxLabel := maxDNSLabelLen - (1 + len(hash))
	if len(label) > maxLabel {
		label = strings.TrimRight(label[:maxLabel], "-")
	}
	if label == "" {
		label = userNamespaceFallback
	}
	return label + "-" + hash
}

// humanNameToLabel reduces a display name to "<first>-<last>" (or the single
// token when only one is present).
func humanNameToLabel(name string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(name)))
	switch len(fields) {
	case 0:
		return ""
	case 1:
		return fields[0]
	default:
		return fields[0] + "-" + fields[len(fields)-1]
	}
}

// sanitizeDNSLabel coerces s into a valid DNS-1123 label fragment: lowercase
// alphanumeric and single dashes, with no leading/trailing dash.
func sanitizeDNSLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonDNSLabelChars.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// subjectHash returns an n-char lowercase-alphanumeric hash of subject.
func subjectHash(subject string, n int) string {
	sum := sha256.Sum256([]byte(subject))
	encoded := strconv.FormatUint(binary.BigEndian.Uint64(sum[:8]), 36)
	if len(encoded) < n {
		encoded += strings.Repeat("0", n)
	}
	return encoded[:n]
}

// ensureUserNamespace resolves (and provisions when missing) the calling user's
// personal namespace. It returns the namespace name.
func (s *Server) ensureUserNamespace(ctx context.Context, actor requestActor) (string, error) {
	if strings.TrimSpace(actor.Subject) == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	return s.ensureNamespaceForUser(ctx, actor.Subject, actor.Name)
}

// authorizeRequestNamespace resolves the namespace a namespaced RPC may act
// in: the caller's personal namespace when the request names none. A different
// namespace is honored for admins, and for regular users only when it is NOT
// another user's personal namespace — shared/system namespaces stay usable
// (e.g. runs created from admin-provisioned sources), but everything a user
// keeps in their personal namespace (MCP servers, skills, saved credentials)
// stays private to them.
//
// As the one exception, a foreign personal namespace is allowed when the
// caller holds a collaborator share on the referenced source there: sharing a
// project is an explicit grant to run it as the owner set it up.
//
// Internal invocations that never passed through the RPC interceptor (no
// actor recorded on the context) keep the requested namespace as-is.
func (s *Server) authorizeRequestNamespace(ctx context.Context, requested string, source *platform.SourceRef) (string, error) {
	reqNS := strings.TrimSpace(requested)
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded && reqNS != "" {
		return reqNS, nil // internal invocation — no RPC interceptor ran
	}
	if reqNS != "" && (actor.Role == "admin" || actor.Role == "owner") {
		return reqNS, nil
	}
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return "", err
	}
	if reqNS == "" || reqNS == namespace {
		return namespace, nil
	}
	// A regular user targeting a foreign namespace: allowed only when it is
	// not a personal namespace provisioned for some (other) user.
	ns := &corev1.Namespace{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: reqNS}, ns); err != nil {
		if k8serrors.IsNotFound(err) {
			// Not a provisioned personal namespace; writes into a truly
			// missing namespace fail downstream anyway.
			return reqNS, nil
		}
		return "", mapK8sError(fmt.Sprintf("get namespace %s", reqNS), err)
	}
	if ns.Labels[userNamespaceLabel] == "true" {
		if s.sourceSharedWithCaller(ctx, reqNS, source) {
			return reqNS, nil
		}
		return "", connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("namespace %q is another user's personal namespace", reqNS))
	}
	return reqNS, nil
}

// sourceSharedWithCaller reports whether the caller holds an explicit
// collaborator-level share on the request's source in namespace. Unowned
// sources do not qualify: inside a personal namespace, the absence of an
// ownership record must not open the owner's namespace to everyone.
func (s *Server) sourceSharedWithCaller(ctx context.Context, namespace string, source *platform.SourceRef) bool {
	if source == nil {
		return false
	}
	resourceType, ok := sourceKindResourceTypes[source.GetKind()]
	if !ok || strings.TrimSpace(source.GetName()) == "" {
		return false
	}
	return s.checkResourceAccess(ctx, resourceType, source.GetName(), namespace) >= AccessCollaborator
}

// ensureNamespaceForUser resolves (and provisions when missing) the personal
// namespace of the user identified by userID/displayName. The mapping is
// persisted on first use so it remains stable even if the user's display name
// later changes. The Namespace object is created idempotently.
func (s *Server) ensureNamespaceForUser(ctx context.Context, userID, displayName string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("user id is required"))
	}

	namespace := ""
	if s.authStore != nil {
		existing, err := s.authStore.GetUserNamespace(ctx, userID)
		if err != nil {
			return "", connect.NewError(connect.CodeInternal, fmt.Errorf("get user namespace: %w", err))
		}
		namespace = strings.TrimSpace(existing)
	}

	if namespace == "" {
		namespace = deriveUserNamespaceName(displayName, userID)
		if s.authStore != nil {
			if err := s.authStore.SetUserNamespace(ctx, userID, namespace); err != nil {
				return "", connect.NewError(connect.CodeInternal, fmt.Errorf("persist user namespace: %w", err))
			}
			// Re-read so a concurrent first-writer's value wins authoritatively.
			if stored, err := s.authStore.GetUserNamespace(ctx, userID); err == nil && strings.TrimSpace(stored) != "" {
				namespace = strings.TrimSpace(stored)
			}
		}
	}

	if err := s.createNamespaceIfMissing(ctx, namespace); err != nil {
		return "", err
	}
	return namespace, nil
}

// createNamespaceIfMissing creates the namespace, treating an existing one as
// success.
func (s *Server) createNamespaceIfMissing(ctx context.Context, namespace string) error {
	ns := &corev1.Namespace{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: namespace}, ns); err == nil {
		return nil
	} else if !k8serrors.IsNotFound(err) {
		return mapK8sError(fmt.Sprintf("get namespace %s", namespace), err)
	}
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: map[string]string{userNamespaceLabel: "true"},
		},
	}
	if err := s.k8sClient.Create(ctx, ns); err != nil && !k8serrors.IsAlreadyExists(err) {
		return mapK8sError(fmt.Sprintf("create namespace %s", namespace), err)
	}
	return nil
}
