// Decides what to show: setup, sign-in, or the app with its pages.

import { useCallback, useEffect, useState } from "react";
import { Layout } from "./components/Layout";
import { api, type Me } from "./lib/api";
import { navigate, usePath } from "./lib/router";
import { Activity } from "./routes/Activity";
import { AddIndexer } from "./routes/AddIndexer";
import { AddMovie } from "./routes/AddMovie";
import { AddShow } from "./routes/AddShow";
import { IndexerForm } from "./routes/IndexerForm";
import { Discover } from "./routes/Discover";
import { Indexers } from "./routes/Indexers";
import { Library } from "./routes/Library";
import { MovieDetail } from "./routes/MovieDetail";
import { MovieReleases } from "./routes/MovieReleases";
import { Movies } from "./routes/Movies";
import { Placeholder } from "./routes/Placeholder";
import { Search } from "./routes/Search";
import { ShowDetail } from "./routes/ShowDetail";
import { ShowReleases } from "./routes/ShowReleases";
import { Shows } from "./routes/Shows";
import { Settings } from "./routes/Settings";
import { Welcome } from "./routes/Welcome";

export function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [error, setError] = useState("");
  const path = usePath();

  const refresh = useCallback(() => {
    api.get<Me>("/api/me").then(setMe).catch(() => setError("Couldn't reach the app. Is it running?"));
  }, []);
  useEffect(refresh, [refresh]);

  // "/" goes to Discover.
  useEffect(() => {
    if (me?.signedIn && path === "/") navigate("/discover");
  }, [me, path]);

  if (error) return <p className="error center">{error}</p>;
  if (!me) return null;
  if (!me.signedIn) return <Welcome firstRun={me.needsSetup} onDone={refresh} />;

  async function signOut() {
    await api.post("/api/logout").catch(() => {});
    refresh();
  }

  return (
    <Layout path={path} username={me.username} onSignOut={signOut}>
      <Page path={path} />
    </Layout>
  );
}

function Page({ path }: { path: string }) {
  if (path.startsWith("/movies")) return <MoviePage path={path} />;
  if (path.startsWith("/shows")) return <ShowPage path={path} />;
  if (path.startsWith("/indexers")) return <IndexerPage path={path} />;
  if (path.startsWith("/activity")) return <Activity />;
  if (path.startsWith("/library")) return <Library />;
  if (path.startsWith("/discover")) return <Discover />;
  if (path.startsWith("/settings")) return <Settings />;
  return <Placeholder title="Not found" text="There's no page at this address." />;
}

function IndexerPage({ path }: { path: string }) {
  const parts = path.split("/").filter(Boolean); // ["indexers", "add", "yts"]
  if (parts[1] === "search") return <Search />;
  if (parts[1] === "add" && parts[2] === "torznab") return <IndexerForm key="torznab" torznab />;
  if (parts[1] === "add" && parts[2]) return <IndexerForm key={parts[2]} definitionId={parts[2]} />;
  if (parts[1] === "add") return <AddIndexer />;
  if (parts[1] === "edit" && parts[2]) return <IndexerForm key={parts[2]} id={parts[2]} />;
  return <Indexers />;
}

function MoviePage({ path }: { path: string }) {
  const parts = path.split("/").filter(Boolean); // ["movies", "10331", "releases"]
  if (parts[1] === "add") return <AddMovie />;
  if (parts[1] && parts[2] === "releases") return <MovieReleases key={parts[1]} id={parts[1]} />;
  if (parts[1]) return <MovieDetail key={parts[1]} id={parts[1]} />;
  return <Movies />;
}

function ShowPage({ path }: { path: string }) {
  const parts = path.split("/").filter(Boolean); // ["shows", "1396", "releases"]
  if (parts[1] === "add") return <AddShow />;
  if (parts[1] && parts[2] === "releases") return <ShowReleases key={path + window.location.search} id={parts[1]} />;
  if (parts[1]) return <ShowDetail key={parts[1]} id={parts[1]} />;
  return <Shows />;
}
