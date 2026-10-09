import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiPost, session } from "../api/client";
import type { User } from "../api/types";
import { Button, ErrorNote } from "../components/ui";

/** Token auth mode: exchange a GitHub personal access token for a session. */
export function SignInPage() {
  const client = useQueryClient();
  const [token, setToken] = useState("");
  const login = useMutation({
    mutationFn: (githubToken: string) =>
      apiPost<{ session_token: string; user: User }>("/v1/auth/token", { github_token: githubToken }),
    onSuccess: async ({ session_token }) => {
      session.set(session_token);
      setToken("");
      await client.invalidateQueries();
    },
  });

  return (
    <div className="mx-auto mt-16 max-w-md rounded-lg border border-slate-200 bg-white p-8">
      <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
        <img src="/favicon.svg" alt="" className="h-6 w-6" /> Sign in to DevAssist
      </h1>
      <p className="mt-2 text-sm text-slate-600">
        Paste a fine-grained GitHub token with <strong>Contents</strong> and <strong>Pull requests</strong> write
        access to the repositories you want to work on. It is stored encrypted and only used to read those
        repositories and open pull requests.
      </p>
      <form
        className="mt-6 space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          login.mutate(token.trim());
        }}
      >
        <label className="block text-sm">
          <span className="font-medium text-slate-700">GitHub token</span>
          <input
            type="password"
            autoComplete="off"
            required
            value={token}
            onChange={(e) => {
              setToken(e.target.value);
            }}
            className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2 font-mono text-sm focus:border-sky-500 focus:outline-none"
            placeholder="github_pat_…"
          />
        </label>
        <Button type="submit" variant="primary" busy={login.isPending} className="w-full">
          Sign in
        </Button>
        <ErrorNote error={login.error} />
      </form>
    </div>
  );
}
