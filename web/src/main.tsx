import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import "./styles.css";

const queryClient = new QueryClient({
  // SSE (useLiveUpdates) drives freshness by invalidating on server events, so
  // the stale window is a loose fallback for a dropped stream, not the primary
  // refresh path.
  defaultOptions: {
    queries: { staleTime: 30_000, refetchOnWindowFocus: true, retry: false },
  },
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </React.StrictMode>,
);
