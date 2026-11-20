// Pieces shared by the forms: sections, text fields, Test buttons.

import type { ReactNode } from "react";
import { ApiError } from "../lib/api";

export type Status = { kind: "ok" | "error"; text: string } | null;

export function errorText(err: unknown) {
  return err instanceof ApiError ? err.message : "Couldn't reach the app.";
}

export function Section({ title, hint, children }: { title: string; hint?: string; children: ReactNode }) {
  return (
    <section className="card section">
      <h2>{title}</h2>
      {hint && <p className="muted small">{hint}</p>}
      {children}
    </section>
  );
}

export type FieldProps = {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: "text" | "password";
  placeholder?: string;
  help?: ReactNode;
  autoComplete?: string;
  inputMode?: "url" | "text";
};

export function TextField({ label, value, onChange, type = "text", placeholder, help, autoComplete, inputMode }: FieldProps) {
  return (
    <label className="field">
      <span>{label}</span>
      <input type={type} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder}
        autoComplete={autoComplete} inputMode={inputMode} spellCheck={false} autoCapitalize="off" />
      {help && <small className="muted">{help}</small>}
    </label>
  );
}

export function TestRow({ busy, status, onTest }: { busy: boolean; status: Status; onTest: () => void }) {
  return (
    <div className="test-row">
      <button type="button" className="button" onClick={onTest} disabled={busy}>{busy ? "Testing…" : "Test"}</button>
      {status && <span className={status.kind === "ok" ? "ok" : "error"} role="status">{status.text}</span>}
    </div>
  );
}
