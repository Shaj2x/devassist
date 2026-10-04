import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { vi } from "vitest";
import { StatusPage } from "./StatusPage";

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <StatusPage />
    </QueryClientProvider>,
  );
}

function mockFetch(status: number, body: unknown) {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  );
}

describe("StatusPage", () => {
  it("shows every dependency as healthy", async () => {
    mockFetch(200, { status: "ok", checks: { postgres: "ok", redis: "ok", kafka: "ok" } });
    renderPage();

    expect(await screen.findAllByText("Healthy")).toHaveLength(3);
    expect(screen.getByText("postgres")).toBeInTheDocument();
  });

  it("shows the failing dependency from a 503 report", async () => {
    mockFetch(503, { status: "unavailable", checks: { postgres: "ok", kafka: "missing topics: job.created" } });
    renderPage();

    expect(await screen.findByText("missing topics: job.created")).toBeInTheDocument();
    expect(screen.getAllByText("Healthy")).toHaveLength(1);
  });

  it("reports an unreachable API", async () => {
    vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("Failed to fetch"));
    renderPage();

    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to fetch");
  });
});
