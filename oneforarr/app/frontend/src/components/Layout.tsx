// The frame around every page: a sidebar on wide screens, a bar of tabs at
// the bottom on phones. CSS decides which one shows (see app.css).

import type { ReactNode } from "react";
import { onLinkClick } from "../lib/router";
import { ActivityIcon, DiscoverIcon, IndexersIcon, MoviesIcon, SettingsIcon, ShowsIcon } from "./Icons";

export const pages = [
  { path: "/discover", label: "Discover", Icon: DiscoverIcon },
  { path: "/movies", label: "Movies", Icon: MoviesIcon },
  { path: "/shows", label: "Shows", Icon: ShowsIcon },
  { path: "/indexers", label: "Indexers", Icon: IndexersIcon },
  { path: "/activity", label: "Activity", Icon: ActivityIcon },
  { path: "/settings", label: "Settings", Icon: SettingsIcon },
];

type Props = {
  path: string;
  username: string;
  onSignOut: () => void;
  children: ReactNode;
};

export function Layout({ path, username, onSignOut, children }: Props) {
  const nav = pages.map(({ path: to, label, Icon }) => (
    <a key={to} href={to} onClick={onLinkClick} className="nav-link" aria-current={path.startsWith(to) ? "page" : undefined}>
      <Icon />
      <span>{label}</span>
    </a>
  ));

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <img src="/icon.svg" alt="" width={28} height={28} />
          OneForArr
        </div>
        <nav aria-label="Main">{nav}</nav>
        <div className="sidebar-foot">
          <span className="muted">{username}</span>
          <button type="button" className="link-button" onClick={onSignOut}>Sign out</button>
        </div>
      </aside>

      <main className="content">{children}</main>

      <nav className="tabbar" aria-label="Main">{nav}</nav>
    </div>
  );
}
