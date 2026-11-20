// A very small router: the current page is the address path (/movies,
// /settings). No library needed for a handful of pages.

import { useEffect, useState, type MouseEvent } from "react";

const changed = "oneforarr:navigate";

// usePath returns the current path and updates when it changes.
export function usePath(): string {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const update = () => setPath(window.location.pathname);
    window.addEventListener("popstate", update);
    window.addEventListener(changed, update);
    return () => {
      window.removeEventListener("popstate", update);
      window.removeEventListener(changed, update);
    };
  }, []);
  return path;
}

// navigate goes to another page without reloading.
export function navigate(to: string) {
  if (to === window.location.pathname + window.location.search) return;
  window.history.pushState(null, "", to);
  window.dispatchEvent(new Event(changed));
  window.scrollTo(0, 0);
}

// onLinkClick lets <a href> links navigate in place, while still allowing
// Ctrl/Cmd-click to open a new tab.
export function onLinkClick(e: MouseEvent<HTMLAnchorElement>) {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  e.preventDefault();
  navigate(e.currentTarget.pathname + e.currentTarget.search);
}
