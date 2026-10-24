import { expect, test } from "vitest";
import { greet } from "./index.js";
test("greet", () => { expect(greet({ name: "bob", age: 3 })).toBe("hello bob"); });
