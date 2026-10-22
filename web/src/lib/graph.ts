// Shapes the build page hands to ui/Graph and ui/Waterfall.
export type GraphNode = { label: string; state: string; deps: number[] };
export type Span = { label: string; start: number; end?: number; state: string };
