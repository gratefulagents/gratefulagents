package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// ShareMyCredentials copies the calling user's saved credential Secrets into
// another user's personal namespace. It is a one-time copy: the recipient gets
// an independent duplicate of the secret material (labeled like any other saved
// credential, so OAuth refresh applies to each copy separately), and later
// edits by either user do not propagate to the other.
func (s *Server) ShareMyCredentials(ctx context.Context, req *platform.ShareMyCredentialsRequest) (*platform.ShareMyCredentialsResponse, error) {
	actor := requestActorFromContext(ctx)
	sourceNS, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}

	names, err := normalizeShareCredentialNames(req.GetCredentials())
	if err != nil {
		return nil, err
	}

	target, err := s.lookupUserByEmail(ctx, req.GetTargetEmail())
	if err != nil {
		return nil, err
	}
	if target.ID == actor.Subject {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot share credentials with yourself"))
	}

	targetNS, err := s.ensureNamespaceForUser(ctx, target.ID, target.Name)
	if err != nil {
		return nil, err
	}
	if targetNS == sourceNS {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot share credentials with yourself"))
	}

	// Read (and cache) every requested credential before mutating anything, so
	// a request naming a missing credential fails before any copy and each
	// target write uses exactly the data that was validated.
	sourceData := make(map[string]map[string][]byte, len(names))
	for _, name := range names {
		secret, err := s.readCredentialSecret(ctx, sourceNS, name)
		if err != nil {
			return nil, err
		}
		data := make(map[string][]byte, len(secret.Data))
		for k, v := range secret.Data {
			data[k] = append([]byte(nil), v...)
		}
		sourceData[name] = data
	}

	// Copy with undo tracking: if a later write fails, revert the earlier ones
	// so the advertised all-or-nothing behavior holds even when the API server
	// rejects a write mid-flight.
	applied := make([]credentialShareUndo, 0, len(names))
	shared := make([]string, 0, len(names))
	for _, name := range names {
		undo, err := s.writeSharedCredentialSecret(ctx, targetNS, name, sourceData[name])
		if err != nil {
			s.rollbackSharedCredentials(ctx, targetNS, applied)
			return nil, err
		}
		applied = append(applied, undo)
		shared = append(shared, name)
	}

	s.notifyCredentialsShared(ctx, actor, target.ID, shared)

	return &platform.ShareMyCredentialsResponse{Shared: shared}, nil
}

// normalizeShareCredentialNames lowercases, validates, and dedupes the
// requested credential names. Valid names are the built-in providers
// (anthropic, openai, copilot, github) and saved integration names.
func normalizeShareCredentialNames(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		name := strings.ToLower(strings.TrimSpace(r))
		if name == "" {
			continue
		}
		if !reservedCredentialNames[name] {
			validated, err := validateIntegrationName(name)
			if err != nil {
				return nil, err
			}
			name = validated
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("select at least one credential to share"))
	}
	return out, nil
}

// lookupUserByEmail resolves a user by exact email match via the integrated
// auth store.
func (s *Server) lookupUserByEmail(ctx context.Context, email string) (*auth.User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target email is required"))
	}
	if s.authStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("user search not available"))
	}
	users, err := s.authStore.SearchUsers(ctx, email, 10)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("searching users: %w", err))
	}
	for _, u := range users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user %q not found", email))
}

// readCredentialSecret loads the usercred-<name> Secret from namespace, mapping
// a missing Secret to a FailedPrecondition error the UI can surface.
func (s *Server) readCredentialSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	secretName := userCredentialSecretName(name)
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("no saved %s credential to share; add it in Settings first", name))
		}
		return nil, mapK8sError(fmt.Sprintf("read credential secret %s/%s", namespace, secretName), err)
	}
	return secret, nil
}

// credentialShareUndo records how to revert one applied target-Secret write:
// delete it when the share created it, or restore the pre-share Secret when
// the share replaced an existing one.
type credentialShareUndo struct {
	name    string
	created bool
	prior   *corev1.Secret
}

// writeSharedCredentialSecret writes data to the usercred-<name> Secret in
// targetNS. The recipient's existing credential of the same name is replaced
// wholesale (not merged) so the copy never mixes material from two accounts.
// It returns the undo record needed to revert the write.
func (s *Server) writeSharedCredentialSecret(ctx context.Context, targetNS, name string, data map[string][]byte) (credentialShareUndo, error) {
	secretName := userCredentialSecretName(name)

	existing := &corev1.Secret{}
	getErr := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: secretName}, existing)
	if getErr != nil {
		if !k8serrors.IsNotFound(getErr) {
			return credentialShareUndo{}, mapK8sError(fmt.Sprintf("read credential secret %s/%s", targetNS, secretName), getErr)
		}
		created := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: targetNS,
			},
			Data: data,
		}
		ensureCredentialLabels(created, name)
		if err := s.k8sClient.Create(ctx, created); err != nil {
			return credentialShareUndo{}, mapK8sError("create credential secret", err)
		}
		return credentialShareUndo{name: name, created: true}, nil
	}

	prior := existing.DeepCopy()
	existing.Data = data
	ensureCredentialLabels(existing, name)
	if err := s.k8sClient.Update(ctx, existing); err != nil {
		return credentialShareUndo{}, mapK8sError("update credential secret", err)
	}
	return credentialShareUndo{name: name, prior: prior}, nil
}

// rollbackSharedCredentials reverts, in reverse order, the target-Secret writes
// applied before a failed share: Secrets the share created are deleted and
// Secrets it replaced are restored to their prior content. Rollback is
// best-effort — it uses a cancellation-detached context so cleanup still runs
// when the request context died, and failures are logged rather than returned
// because the caller's original error is the one the user must see.
func (s *Server) rollbackSharedCredentials(ctx context.Context, targetNS string, applied []credentialShareUndo) {
	ctx = context.WithoutCancel(ctx)
	for i := len(applied) - 1; i >= 0; i-- {
		undo := applied[i]
		secretName := userCredentialSecretName(undo.name)
		if undo.created {
			created := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: targetNS}}
			if err := s.k8sClient.Delete(ctx, created); err != nil && !k8serrors.IsNotFound(err) {
				log.Printf("WARN: rollback of shared credential %s/%s: delete failed: %v", targetNS, secretName, err)
			}
			continue
		}
		current := &corev1.Secret{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: secretName}, current); err != nil {
			log.Printf("WARN: rollback of shared credential %s/%s: read failed: %v", targetNS, secretName, err)
			continue
		}
		current.Data = undo.prior.Data
		current.Labels = undo.prior.Labels
		current.Annotations = undo.prior.Annotations
		if err := s.k8sClient.Update(ctx, current); err != nil {
			log.Printf("WARN: rollback of shared credential %s/%s: restore failed: %v", targetNS, secretName, err)
		}
	}
}

// notifyCredentialsShared records a best-effort notification for the recipient.
func (s *Server) notifyCredentialsShared(ctx context.Context, actor requestActor, targetUserID string, names []string) {
	if s.stateStore == nil {
		return
	}
	sender := strings.TrimSpace(actor.Name)
	if sender == "" {
		sender = actor.Subject
	}
	if err := s.stateStore.CreateNotification(ctx, &store.Notification{
		UserID:  targetUserID,
		Type:    "credentials_shared",
		Title:   "Credentials shared with you",
		Body:    fmt.Sprintf("%s shared their %s credentials with you", sender, strings.Join(names, ", ")),
		ActorID: actor.Subject,
	}); err != nil {
		log.Printf("WARN: failed to create credentials-shared notification: %v", err)
	}
}
