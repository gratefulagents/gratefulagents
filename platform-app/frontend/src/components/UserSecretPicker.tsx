export interface UserSecretOption {
  name: string;
  keys: string[];
}

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50";

export function UserSecretPicker({
  id,
  value,
  secrets,
  onChange,
  onOpen,
  loading = false,
  ariaLabel,
}: {
  id?: string;
  value: string;
  secrets: UserSecretOption[];
  onChange: (value: string) => void;
  onOpen?: () => void;
  loading?: boolean;
  ariaLabel?: string;
}) {
  const currentMissing = Boolean(value && !secrets.some((secret) => secret.name === value));
  return (
    <select
      id={id}
      aria-label={ariaLabel}
      value={value}
      onChange={(event) => onChange(event.target.value)}
      onPointerDown={onOpen}
      onKeyDown={(event) => {
        if (["Enter", " ", "ArrowDown", "ArrowUp"].includes(event.key)) onOpen?.();
      }}
      className={selectClassName}
    >
      <option value="">{loading ? "Loading your secrets…" : "No secret"}</option>
      {currentMissing ? <option value={value}>{value} (not found)</option> : null}
      {secrets.map((secret) => (
        <option key={secret.name} value={secret.name}>
          {secret.name}
        </option>
      ))}
    </select>
  );
}

export function UserSecretKeyPicker({
  id,
  value,
  secretName,
  secrets,
  onChange,
  ariaLabel,
}: {
  id?: string;
  value: string;
  secretName: string;
  secrets: UserSecretOption[];
  onChange: (value: string) => void;
  ariaLabel?: string;
}) {
  const keys = secrets.find((secret) => secret.name === secretName)?.keys ?? [];
  const currentMissing = Boolean(value && !keys.includes(value));
  return (
    <select
      id={id}
      aria-label={ariaLabel}
      value={value}
      onChange={(event) => onChange(event.target.value)}
      className={selectClassName}
      disabled={!secretName}
    >
      <option value="">Select key</option>
      {currentMissing ? <option value={value}>{value} (not found)</option> : null}
      {keys.map((key) => (
        <option key={key} value={key}>
          {key}
        </option>
      ))}
    </select>
  );
}
