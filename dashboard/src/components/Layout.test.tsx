import { screen } from "@testing-library/react";
import { ME, mockApi, renderAt, reply } from "../test/api";
import { Layout } from "./Layout";

describe("Layout", () => {
  it("shows navigation and the user in dev mode", async () => {
    mockApi({ "GET /v1/auth/me": ME });
    renderAt("/", "/", <Layout />);

    expect(await screen.findByRole("link", { name: "Jobs" })).toBeInTheDocument();
    expect(screen.getByText("demo")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign out" })).not.toBeInTheDocument();
  });

  it("asks for a GitHub token when the API requires sign-in", async () => {
    mockApi({ "GET /v1/auth/me": reply(401, { detail: "sign in with a GitHub token first" }) });
    renderAt("/", "/", <Layout />);

    expect(await screen.findByRole("heading", { name: /Sign in to DevAssist/ })).toBeInTheDocument();
    expect(screen.getByLabelText("GitHub token")).toHaveAttribute("type", "password");
    expect(screen.queryByRole("link", { name: "Jobs" })).not.toBeInTheDocument();
  });
});

