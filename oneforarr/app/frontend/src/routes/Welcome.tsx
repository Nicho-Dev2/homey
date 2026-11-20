// The first screen: create the owner account (first run) or sign in.

import { useState, type FormEvent } from "react";
import { api, ApiError } from "../lib/api";

type Props = { firstRun: boolean; onDone: () => void };

export function Welcome({ firstRun, onDone }: Props) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    if (firstRun && password !== repeat) {
      setError("The two passwords don't match.");
      return;
    }
    setBusy(true);
    try {
      await api.post(firstRun ? "/api/setup" : "/api/login", { username, password });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Couldn't reach the app.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="welcome">
      <form className="card welcome-card" onSubmit={submit}>
        <div className="brand">
          <img src="/icon.svg" alt="" width={32} height={32} />
          OneForArr
        </div>
        <h1>{firstRun ? "Create the owner account" : "Sign in"}</h1>
        {firstRun && <p className="muted">This is the only account. Anyone who opens the app before you do could create it, so do this now.</p>}

        <label className="field">
          <span>Username</span>
          <input autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required autoFocus />
        </label>
        <label className="field">
          <span>Password</span>
          <input type="password" autoComplete={firstRun ? "new-password" : "current-password"}
            value={password} onChange={(e) => setPassword(e.target.value)} required minLength={firstRun ? 10 : undefined} />
          {firstRun && <small className="muted">At least 10 characters.</small>}
        </label>
        {firstRun && (
          <label className="field">
            <span>Password again</span>
            <input type="password" autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} required />
          </label>
        )}

        {error && <p className="error" role="alert">{error}</p>}
        <button className="button primary wide" disabled={busy}>
          {busy ? "One moment…" : firstRun ? "Create account" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
