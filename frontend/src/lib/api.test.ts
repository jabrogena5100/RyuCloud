import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, apiFetch, describeError } from "./api";

function mockFetch(status: number, body: unknown) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("apiFetch", () => {
  it("calls the same-origin API prefix and returns JSON", async () => {
    const fetchMock = mockFetch(200, { id: "u1" });
    await expect(apiFetch("/auth/me")).resolves.toEqual({ id: "u1" });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/auth/me", expect.objectContaining({ credentials: "same-origin" }));
  });

  it("sends JSON content type when there is a body", async () => {
    const fetchMock = mockFetch(200, {});
    await apiFetch("/auth/login", { method: "POST", body: "{}" });
    const headers = fetchMock.mock.calls[0][1].headers as Headers;
    expect(headers.get("Content-Type")).toBe("application/json");
  });

  it("returns undefined for 204 responses", async () => {
    mockFetch(204, null);
    await expect(apiFetch("/auth/logout", { method: "POST" })).resolves.toBeUndefined();
  });

  it("throws ApiError built from the error envelope", async () => {
    mockFetch(401, { error: { code: "invalid_credentials", message: "Invalid email or password." } });
    const error = await apiFetch("/auth/login").catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 401, code: "invalid_credentials", message: "Invalid email or password." });
  });
});

describe("describeError", () => {
  it("formats validation details by field", () => {
    const error = new ApiError(422, "validation_error", "The request is invalid.", [
      { loc: ["body", "password"], msg: "String should have at least 8 characters", type: "string_too_short" },
      { loc: ["body", "display_name"], msg: "Field required", type: "missing" },
    ]);
    expect(describeError(error)).toBe(
      "password: String should have at least 8 characters; display name: Field required",
    );
  });

  it("falls back to a generic message for unknown errors", () => {
    expect(describeError(new TypeError("Failed to fetch"))).toBe("Something went wrong. Please try again.");
  });
});
