type t = { x : int; y : int }
let make x y = { x; y }
let add a b = { x = a.x + b.x; y = a.y + b.y }
external mix : int -> int -> int = "geom_mix"
let hash v = mix v.x v.y
