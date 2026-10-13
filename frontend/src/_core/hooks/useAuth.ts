import { api, LOGIN_PATH } from "@/api";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo } from "react";

type UseAuthOptions = {
  redirectOnUnauthenticated?: boolean;
  redirectPath?: string;
};

export const AUTH_ME_QUERY_KEY = ["auth", "me"] as const;

export function useAuth(options?: UseAuthOptions) {
  const { redirectOnUnauthenticated = false, redirectPath = LOGIN_PATH } =
    options ?? {};
  const queryClient = useQueryClient();

  const meQuery = useQuery({
    queryKey: AUTH_ME_QUERY_KEY,
    queryFn: () => api.auth.me(),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const logout = useCallback(async () => {
    try {
      await api.auth.logout();
    } finally {
      queryClient.setQueryData(AUTH_ME_QUERY_KEY, { user: null });
      await queryClient.invalidateQueries({ queryKey: AUTH_ME_QUERY_KEY });
    }
  }, [queryClient]);

  const state = useMemo(
    () => ({
      user: meQuery.data?.user ?? null,
      loading: meQuery.isLoading,
      error: meQuery.error ?? null,
      isAuthenticated: Boolean(meQuery.data?.user),
    }),
    [meQuery.data, meQuery.error, meQuery.isLoading]
  );

  useEffect(() => {
    if (!redirectOnUnauthenticated) return;
    if (meQuery.isLoading) return;
    if (state.user) return;
    if (typeof window === "undefined") return;
    if (window.location.pathname === redirectPath) return;

    window.location.href = redirectPath;
  }, [redirectOnUnauthenticated, redirectPath, meQuery.isLoading, state.user]);

  return {
    ...state,
    refresh: () => meQuery.refetch(),
    logout,
  };
}
