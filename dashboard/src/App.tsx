import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { JobPage } from "./pages/JobPage";
import { JobsPage } from "./pages/JobsPage";
import { RepositoriesPage } from "./pages/RepositoriesPage";
import { StatusPage } from "./pages/StatusPage";

function defaultClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      // Don't retry client errors (404, 409...): they will not fix themselves.
      queries: { retry: (count, error) => count < 2 && !("status" in error && Number(error.status) < 500) },
    },
  });
}

export function App({ queryClient = defaultClient() }: { queryClient?: QueryClient }) {
  return (
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Routes>
          <Route element={<Layout />}>
            <Route index element={<RepositoriesPage />} />
            <Route path="jobs" element={<JobsPage />} />
            <Route path="jobs/:jobId" element={<JobPage />} />
            <Route path="status" element={<StatusPage />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
