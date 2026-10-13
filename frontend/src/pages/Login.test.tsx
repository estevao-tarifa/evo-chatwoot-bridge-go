/**
 * F6 — `/api/v1/auth/state` drives setup vs login rendering.
 */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Login from "./Login";

function stateResponse(state: string): Response {
  return new Response(JSON.stringify({ state }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

describe("F6 login page state", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("calls /api/v1/auth/state", async () => {
    fetchMock.mockResolvedValue(stateResponse("ready"));
    render(<Login onSuccess={() => {}} />);
    await screen.findByText("Faça login para acessar o painel de gerenciamento");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/auth/state");
  });

  it("renders the setup form when state is needs_setup", async () => {
    fetchMock.mockResolvedValue(stateResponse("needs_setup"));
    render(<Login onSuccess={() => {}} />);
    expect(await screen.findByText("Configuração Inicial")).toBeInTheDocument();
    expect(
      screen.getByText("Crie sua conta de administrador para acessar o painel")
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Criar Conta" })).toBeInTheDocument();
  });

  it("renders the login form when state is ready", async () => {
    fetchMock.mockResolvedValue(stateResponse("ready"));
    render(<Login onSuccess={() => {}} />);
    expect(
      await screen.findByText("Faça login para acessar o painel de gerenciamento")
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Entrar" })).toBeInTheDocument();
  });
});
