import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import { AppErrorBoundary } from "./AppErrorBoundary";
import "./styles.css";
import "./aexp-theme.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2500,
      refetchOnWindowFocus: false,
      retry: 1
    }
  }
});

try {
  const root = document.getElementById("root");
  if (!root) throw new Error("aexp UI root element is missing");
  ReactDOM.createRoot(root).render(
    <React.StrictMode>
      <AppErrorBoundary>
        <QueryClientProvider client={queryClient}>
          <App />
        </QueryClientProvider>
      </AppErrorBoundary>
    </React.StrictMode>
  );
} catch (cause) {
  const message = cause instanceof Error ? cause.message : String(cause);
  document.body.innerHTML = `<main style="font:16px system-ui;padding:32px;color:#3b1d1d"><h1>aexp UI failed to start</h1><p>${message.replace(/[<>&]/g, "")}</p><p>Reload after restarting the aexp backend. Your experiment process is unaffected.</p></main>`;
}
