open Cmdliner

let run n =
  let v = Geom.Vec.(add (make n 2) (make 1 1)) in
  let json = `Assoc [ ("x", `Int v.x); ("y", `Int v.y); ("hash", `Int (Geom.Vec.hash v)) ] in
  Lwt_main.run (Lwt_io.printl (Yojson.Safe.to_string json));
  Fmt.pr "vec %a@." Geom.Show.pp v

let n = Arg.(value & opt int 3 & info [ "n" ])
let () = exit (Cmd.eval (Cmd.v (Cmd.info "demo") Term.(const run $ n)))
