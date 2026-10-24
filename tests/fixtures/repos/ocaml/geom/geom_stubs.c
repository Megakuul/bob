#include <caml/mlvalues.h>
#include "cfoo.h"
value geom_mix(value a, value b) { return Val_long(cfoo_mix(Long_val(a), Long_val(b))); }
