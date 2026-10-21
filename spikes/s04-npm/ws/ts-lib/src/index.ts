import { z } from "zod";
export const User = z.object({ name: z.string().min(1), age: z.number().int().nonnegative() });
export type User = z.infer<typeof User>;
export function greet(u: User): string {
  return `hello ${User.parse(u).name}`;
}
