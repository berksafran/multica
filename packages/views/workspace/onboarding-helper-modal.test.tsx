import type { ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../locales/en/common.json";
import enOnboarding from "../locales/en/onboarding.json";

// --- Hoisted mock refs (the rendered component pulls values from these
// each render, so test cases can mutate them between renders to flip
// the modal's gate conditions without re-mounting the QueryClient.) ---
const userRef = vi.hoisted(() => ({
  current: null as {
    id: string;
    onboarded_at: string | null;
  } | null,
}));
const workspaceRef = vi.hoisted(() => ({
  current: null as { id: string; slug: string } | null,
}));
const runtimesRef = vi.hoisted(() => ({
  current: [] as Array<{ id: string; name: string }>,
}));
const mockBootstrap = vi.hoisted(() => vi.fn());
const mockNavigate = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/auth", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/auth")>(
      "@multica/core/auth",
    );
  const useAuthStore = Object.assign(
    (sel?: (s: { user: typeof userRef.current }) => unknown) =>
      sel ? sel({ user: userRef.current }) : { user: userRef.current },
    { getState: () => ({ user: userRef.current }) },
  );
  return { ...actual, useAuthStore };
});

vi.mock("@multica/core/paths", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/paths")>(
      "@multica/core/paths",
    );
  return {
    ...actual,
    useCurrentWorkspace: () => workspaceRef.current,
  };
});

vi.mock("@multica/core/onboarding", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/onboarding")>(
      "@multica/core/onboarding",
    );
  return {
    ...actual,
    bootstrapRuntimeOnboarding: mockBootstrap,
  };
});

vi.mock("@multica/core/runtimes", () => ({
  // The component calls useQuery({ ...runtimeListOptions(wsId), enabled })
  // We don't run the real query — we synthesize a queryFn that returns
  // whatever runtimesRef holds, so a test can flip the runtime list by
  // mutating the ref.
  runtimeListOptions: (wsId: string) => ({
    queryKey: ["runtimes", wsId],
    queryFn: () => runtimesRef.current,
  }),
}));

vi.mock("../navigation", () => ({
  useNavigation: () => ({ push: mockNavigate, replace: mockNavigate }),
}));

// Import AFTER the mocks so the component picks up the mocked modules.
import { OnboardingHelperModal } from "./onboarding-helper-modal";

const TEST_RESOURCES = {
  en: { common: enCommon, onboarding: enOnboarding },
};

function renderModal() {
  // Disable retries so a thrown mockBootstrap surfaces immediately.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <OnboardingHelperModal />
      </I18nProvider>
    </QueryClientProvider>,
  );
}

function Wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        {children}
      </I18nProvider>
    </QueryClientProvider>
  );
}

describe("OnboardingHelperModal — gating", () => {
  beforeEach(() => {
    userRef.current = null;
    workspaceRef.current = null;
    runtimesRef.current = [];
    mockBootstrap.mockReset();
    mockNavigate.mockReset();
  });

  it("renders nothing when user is not yet loaded", () => {
    renderModal();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders nothing when user is already onboarded", () => {
    userRef.current = { id: "u1", onboarded_at: "2026-05-01T00:00:00Z" };
    workspaceRef.current = { id: "w1", slug: "acme" };
    runtimesRef.current = [{ id: "r1", name: "local" }];
    renderModal();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders nothing when workspace is missing", () => {
    userRef.current = { id: "u1", onboarded_at: null };
    workspaceRef.current = null;
    runtimesRef.current = [{ id: "r1", name: "local" }];
    renderModal();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders nothing when workspace has no runtime yet", async () => {
    // No runtime → modal can't bootstrap, so it stays hidden until the
    // runtime list resolves with at least one entry. This guards against
    // showing a dead modal when daemon disconnect drops the count to zero
    // mid-session.
    userRef.current = { id: "u1", onboarded_at: null };
    workspaceRef.current = { id: "w1", slug: "acme" };
    runtimesRef.current = [];
    renderModal();
    // Give the query one tick to settle, then assert silence.
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders dialog when all four conditions hold", async () => {
    userRef.current = { id: "u1", onboarded_at: null };
    workspaceRef.current = { id: "w1", slug: "acme" };
    runtimesRef.current = [{ id: "r1", name: "local" }];
    renderModal();
    expect(
      await screen.findByText(/Meet Multica Helper/i),
    ).toBeInTheDocument();
  });
});

describe("OnboardingHelperModal — three starter cards", () => {
  beforeEach(() => {
    userRef.current = { id: "u1", onboarded_at: null };
    workspaceRef.current = { id: "w1", slug: "acme" };
    runtimesRef.current = [{ id: "r1", name: "local" }];
    mockBootstrap.mockReset();
    mockNavigate.mockReset();
  });

  it("renders all three starter cards with their localized titles", async () => {
    renderModal();
    await screen.findByText(/Meet Multica Helper/i);
    // Card titles from packages/views/locales/en/onboarding.json
    expect(screen.getByText(/Introduce me to Multica/i)).toBeInTheDocument();
    expect(
      screen.getByText(/Show me how to assign an issue/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Help me create a second agent/i),
    ).toBeInTheDocument();
  });

  it("clicking a card calls bootstrap with that card's prompt and navigates to the issue", async () => {
    mockBootstrap.mockResolvedValue({
      workspace_id: "w1",
      agent_id: "a1",
      issue_id: "i1",
    });
    renderModal();
    await screen.findByText(/Meet Multica Helper/i);
    const user = userEvent.setup();
    await user.click(
      screen.getByText(/Show me how to assign an issue/i).closest("button")!,
    );
    await waitFor(() => expect(mockBootstrap).toHaveBeenCalledTimes(1));
    const [wsId, rtId, prompt] = mockBootstrap.mock.calls[0]!;
    expect(wsId).toBe("w1");
    expect(rtId).toBe("r1");
    // The prompt text comes from the i18n value for cards.assign.prompt.
    expect(prompt).toMatch(/assign.*issue.*agent/i);
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledTimes(1));
    // Navigation lands on the new onboarding issue's detail page.
    expect(mockNavigate.mock.calls[0]![0]).toContain("/acme/issues/i1");
  });

  it("disables the other cards while one card's mutation is in flight", async () => {
    // Hold the promise open so all three cards see a pending state.
    let resolve!: (v: {
      workspace_id: string;
      agent_id: string;
      issue_id: string;
    }) => void;
    mockBootstrap.mockReturnValue(
      new Promise<{
        workspace_id: string;
        agent_id: string;
        issue_id: string;
      }>((r) => {
        resolve = r;
      }),
    );
    renderModal();
    await screen.findByText(/Meet Multica Helper/i);
    const user = userEvent.setup();
    const introButton = screen
      .getByText(/Introduce me to Multica/i)
      .closest("button")!;
    const assignButton = screen
      .getByText(/Show me how to assign an issue/i)
      .closest("button")!;
    await user.click(introButton);
    // The other two cards are disabled while the first is in flight.
    await waitFor(() => expect(assignButton).toBeDisabled());
    // Let the test finish gracefully.
    resolve({ workspace_id: "w1", agent_id: "a1", issue_id: "i1" });
  });

  it("surfaces an inline error message if bootstrap throws, modal stays open", async () => {
    mockBootstrap.mockRejectedValue(new Error("backend exploded"));
    renderModal();
    await screen.findByText(/Meet Multica Helper/i);
    const user = userEvent.setup();
    await user.click(
      screen.getByText(/Introduce me to Multica/i).closest("button")!,
    );
    expect(await screen.findByText(/backend exploded/i)).toBeInTheDocument();
    // Modal is still rendered — the error didn't unmount the dialog.
    expect(screen.getByText(/Meet Multica Helper/i)).toBeInTheDocument();
    // Navigation never fired.
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});

// Sanity check: the dialog has no close button rendered (the modal is
// designed to be dismissed implicitly by `me.onboarded_at` flipping
// non-null after a successful bootstrap, not by user action). Failing
// this test means someone re-introduced a close button — `dismissible`
// behavior must be re-examined alongside that.
describe("OnboardingHelperModal — dismissibility", () => {
  it("has no close button", async () => {
    userRef.current = { id: "u1", onboarded_at: null };
    workspaceRef.current = { id: "w1", slug: "acme" };
    runtimesRef.current = [{ id: "r1", name: "local" }];
    render(
      <Wrapper>
        <OnboardingHelperModal />
      </Wrapper>,
    );
    await screen.findByText(/Meet Multica Helper/i);
    // The shadcn DialogContent close button has data-slot="dialog-close".
    expect(
      document.querySelector("[data-slot='dialog-close']"),
    ).toBeNull();
  });
});
