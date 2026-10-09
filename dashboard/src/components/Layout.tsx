import { useQueryClient } from "@tanstack/react-query";
import { NavLink, Outlet } from "react-router-dom";
import { ApiError, apiPost, session } from "../api/client";
import { useMe, useNotifications } from "../api/queries";
import { SignInPage } from "../pages/SignInPage";
import { cx } from "../lib/ui";
import { Button, Loading } from "./ui";

const navItems = [
  { to: "/", label: "Repositories", end: true },
  { to: "/jobs", label: "Jobs", end: false },
  { to: "/status", label: "System status", end: true },
];

export function Layout() {
  const { data: me, error, isPending } = useMe();
  const needsSignIn = error instanceof ApiError && error.status === 401;
  useNotifications(me !== undefined);

  return (
    <div className="min-h-screen">
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-4 px-4 sm:gap-8 sm:px-6">
          <span className="flex shrink-0 items-center gap-2 font-semibold tracking-tight">
            <img src="/favicon.svg" alt="" className="h-6 w-6" />
            DevAssist
          </span>
          {me && (
            <nav className="flex min-w-0 gap-1 overflow-x-auto whitespace-nowrap text-sm">
              {navItems.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end}
                  className={({ isActive }) =>
                    cx(
                      "rounded-md px-3 py-1.5",
                      isActive ? "bg-slate-100 font-medium text-slate-900" : "text-slate-600 hover:text-slate-900",
                    )
                  }
                >
                  {item.label}
                </NavLink>
              ))}
            </nav>
          )}
          {me && <UserMenu login={me.user.login} avatar={me.user.avatar_url} tokenMode={me.auth_mode === "token"} />}
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-4 py-8 sm:px-6">
        {isPending ? <Loading /> : needsSignIn ? <SignInPage /> : <Outlet />}
      </main>
    </div>
  );
}

function UserMenu({ login, avatar, tokenMode }: { login: string; avatar: string | null; tokenMode: boolean }) {
  const client = useQueryClient();
  return (
    <div className="ml-auto hidden items-center gap-3 text-sm text-slate-600 sm:flex">
      {avatar && <img src={avatar} alt="" className="h-6 w-6 rounded-full" />}
      <span>{login}</span>
      {tokenMode && (
        <Button
          variant="ghost"
          onClick={() => {
            void apiPost("/v1/auth/logout").finally(() => {
              session.set(null);
              client.clear();
              void client.invalidateQueries();
            });
          }}
        >
          Sign out
        </Button>
      )}
    </div>
  );
}
