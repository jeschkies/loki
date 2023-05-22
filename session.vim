let SessionLoad = 1
let s:so_save = &g:so | let s:siso_save = &g:siso | setg so=0 siso=0 | setl so=-1 siso=-1
let v:this_session=expand("<sfile>:p")
silent only
silent tabonly
cd ~/src/loki
if expand('%') == '' && !&modified && line('$') <= 1 && getline(1) == ''
  let s:wipebuf = bufnr('%')
endif
let s:shortmess_save = &shortmess
if &shortmess =~ 'A'
  set shortmess=aoOA
else
  set shortmess=aoO
endif
badd +155 pkg/planning/logical/plan.go
badd +95 pkg/planning/logical/optimize.go
badd +153 pkg/logql/shardmapper.go
badd +1841 pkg/logql/syntax/ast.go
badd +270 pkg/logql/rangemapper.go
badd +180 pkg/querier/queryrange/shard_resolver.go
badd +107 ~/src/loki/pkg/logql/downstream.go
badd +11 ~/src/loki/pkg/logql/optimize.go
badd +10 ~/src/loki/pkg/logql/step_evaluator.go
badd +219 ~/src/loki/vendor/github.com/prometheus/prometheus/promql/value.go
badd +353 ~/src/loki/pkg/logql/engine.go
badd +135 ~/src/loki/pkg/logql/evaluator.go
argglobal
%argdel
$argadd pkg/planning/logical/plan.go
edit ~/src/loki/pkg/logql/downstream.go
argglobal
balt pkg/logql/shardmapper.go
setlocal fdm=manual
setlocal fde=0
setlocal fmr={{{,}}}
setlocal fdi=#
setlocal fdl=0
setlocal fml=1
setlocal fdn=20
setlocal fen
silent! normal! zE
let &fdl = &fdl
let s:l = 315 - ((33 * winheight(0) + 25) / 51)
if s:l < 1 | let s:l = 1 | endif
keepjumps exe s:l
normal! zt
keepjumps 315
normal! 025|
tabnext 1
if exists('s:wipebuf') && len(win_findbuf(s:wipebuf)) == 0 && getbufvar(s:wipebuf, '&buftype') isnot# 'terminal'
  silent exe 'bwipe ' . s:wipebuf
endif
unlet! s:wipebuf
set winheight=1 winwidth=20
let &shortmess = s:shortmess_save
let s:sx = expand("<sfile>:p:r")."x.vim"
if filereadable(s:sx)
  exe "source " . fnameescape(s:sx)
endif
let &g:so = s:so_save | let &g:siso = s:siso_save
set hlsearch
doautoall SessionLoadPost
unlet SessionLoad
" vim: set ft=vim :
