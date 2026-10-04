import { useQuery } from "@tanstack/react-query";
import { ApiError, apiGet } from "./client";

export interface ReadinessReport {
  status: "ok" | "unavailable";
  checks: Record<string, string>;
}

/** Polls the API's /readyz. A 503 still carries a report, so unwrap it. */
export function useReadiness() {
  return useQuery({
    queryKey: ["readyz"],
    queryFn: async (): Promise<ReadinessReport> => {
      try {
        return await apiGet<ReadinessReport>("/readyz");
      } catch (error) {
        if (error instanceof ApiError && error.status === 503 && isReport(error.body)) {
          return error.body;
        }
        throw error;
      }
    },
    refetchInterval: 5000,
    retry: false,
  });
}

function isReport(value: unknown): value is ReadinessReport {
  return typeof value === "object" && value !== null && "checks" in value && "status" in value;
}
