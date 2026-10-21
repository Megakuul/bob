(* TAP test for geom. *)
let () =
  let v = Geom.Vec.(add (make 1 2) (make 3 4)) in
  print_endline "TAP version 14";
  print_endline "1..2";
  Printf.printf "%s 1 - add\n" (if v.x = 4 && v.y = 6 then "ok" else "not ok");
  Printf.printf "%s 2 - hash via C stub\n" (if Geom.Vec.hash v = 130 then "ok" else "not ok");
  if not (v.x = 4 && v.y = 6 && Geom.Vec.hash v = 130) then exit 1
