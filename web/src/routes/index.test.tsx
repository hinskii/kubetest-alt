import { screen, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";

import { server } from "@/test/server";
import { renderRoute } from "@/test/render";

describe("home", () => {
  it("lists clusters with their state, linking to the cluster pages", async () => {
    renderRoute("/");
    const dev = await screen.findByTestId("cluster-dev");
    expect(dev).toHaveAttribute("href", "/clusters/dev");
    expect(within(dev).getByText("connected")).toBeInTheDocument();
    const prod = screen.getByTestId("cluster-prod");
    expect(within(prod).getByText("unreachable")).toBeInTheDocument();
    expect(within(prod).getByText("dial tcp: i/o timeout")).toBeInTheDocument();
  });

  it("shows the signed-in user and role", async () => {
    renderRoute("/");
    const me = await screen.findByTestId("me");
    expect(me).toHaveTextContent("dev@example.com");
    expect(me).toHaveTextContent("developer");
  });

  it("says so when no clusters are configured", async () => {
    server.use(http.get("*/api/v1/clusters", () => HttpResponse.json([])));
    renderRoute("/");
    expect(await screen.findByText(/No clusters are configured/)).toBeInTheDocument();
  });

  it("shows the API's error", async () => {
    server.use(
      http.get("*/api/v1/clusters", () => HttpResponse.json({ error: "registry broken" }, { status: 500 })),
    );
    renderRoute("/");
    expect(await screen.findByRole("alert")).toHaveTextContent("registry broken");
  });
});

describe("unknown page", () => {
  it("renders the not-found page", async () => {
    renderRoute("/nope");
    expect(await screen.findByText("Page not found")).toBeInTheDocument();
  });
});
