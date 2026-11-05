"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { api } from "./api";
import type { User } from "./types";

type AuthStatus = "loading" | "authenticated" | "anonymous";

type AuthContextValue = {
  user: User | null;
  status: AuthStatus;
  login: (email: string, password: string) => Promise<void>;
  register: (input: { email: string; password: string; displayName: string }) => Promise<void>;
  logout: () => Promise<void>;
};

const AuthContext = createContext<AuthContextValue | null>(null);

/**
 * Holds the signed-in user in memory only. The session itself lives in an
 * HttpOnly cookie, so on load we ask the API who we are.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [status, setStatus] = useState<AuthStatus>("loading");

  const signedIn = useCallback((next: User | null) => {
    setUser(next);
    setStatus(next ? "authenticated" : "anonymous");
  }, []);

  useEffect(() => {
    let cancelled = false;
    api
      .me()
      .then((me) => !cancelled && signedIn(me))
      .catch(() => !cancelled && signedIn(null));
    return () => {
      cancelled = true;
    };
  }, [signedIn]);

  const login = useCallback(
    async (email: string, password: string) => signedIn(await api.login({ email, password })),
    [signedIn],
  );

  const register = useCallback(
    async ({ email, password, displayName }: { email: string; password: string; displayName: string }) =>
      signedIn(await api.register({ email, password, display_name: displayName })),
    [signedIn],
  );

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      signedIn(null);
    }
  }, [signedIn]);

  const value = useMemo(
    () => ({ user, status, login, register, logout }),
    [user, status, login, register, logout],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error("useAuth must be used inside <AuthProvider>");
  }
  return context;
}
