import { useState } from "react";
export default function App() {
  const [n, setN] = useState(0);
  return <button id="b" onClick={() => setN(n + 1)}>marker-v1 count {n}</button>;
}
