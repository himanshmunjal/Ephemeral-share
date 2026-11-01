import { Component } from "react";
import { reportError } from "../lib/monitoring";

/** Last-resort screen if rendering crashes, instead of a blank page. */
export default class ErrorBoundary extends Component {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error) {
    reportError(error);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="mx-auto mt-24 max-w-md px-4 text-center">
        <h1 className="font-display text-2xl font-semibold text-slate-900 dark:text-slate-50">That didn’t go to plan</h1>
        <p className="mt-2 text-sm text-slate-500 dark:text-slate-400">
          Something broke on our side. Reloading usually fixes it — your session, if it’s still running, will be right where you left it.
        </p>
        <button onClick={() => window.location.reload()} className="btn-primary mt-6">
          Reload
        </button>
      </div>
    );
  }
}
