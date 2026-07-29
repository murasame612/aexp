import { Component, type ErrorInfo, type ReactNode } from "react";

type State = { error: Error | null };

export class AppErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("aexp UI render failed", error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <main className="app-crash-screen" role="alert">
        <div>
          <span>aexp UI v2</span>
          <h1>页面遇到异常，但服务仍在运行</h1>
          <p>{this.state.error.message || "Unknown UI error"}</p>
          <button type="button" onClick={() => window.location.reload()}>重新加载</button>
        </div>
      </main>
    );
  }
}
