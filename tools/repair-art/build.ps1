# Builds the repair panel's close-up art (LLM-696): the picture of each damaged
# site, broken and mended, and the pieces of the three mini-games. Every pixel
# is drawn here — no pack art is copied — in the Mana Seed packs' colours, at
# one stage pixel per art pixel (repair_stage.gd draws them at that scale).
#
#   pwsh tools/repair-art/build.ps1            # writes client/assets/repair/*.png
#
# Each element seeds its own random numbers from its coordinates, so a post or
# a plank comes out the same in the broken and the mended picture and the
# reveal line only sweeps what the repair changes. Rerunning writes the same
# PNGs. After a change, open the client project once (godot --headless
# --editor --quit) so Godot writes the .import sidecars, and commit both.

param([string]$Dest = (Join-Path $PSScriptRoot '..\..\client\assets\repair'))

Add-Type -AssemblyName System.Drawing

function C([string]$hex, [int]$a = 255) {
    [System.Drawing.Color]::FromArgb($a, [Convert]::ToInt32($hex.Substring(0, 2), 16), [Convert]::ToInt32($hex.Substring(2, 2), 16), [Convert]::ToInt32($hex.Substring(4, 2), 16))
}

# Wood: the ranch fence's four colours plus one lighter tone.
$OUT = C '2A2222'; $DARK = C '44392E'; $MID = C '774827'; $HI = C 'A07030'; $HI2 = C 'C8934A'
# The Mana Seed tool ramps: warm iron, and tool-handle wood.
$I0 = C '332C28'; $I1 = C '54504E'; $I2 = C '82807D'; $I3 = C 'B7B5AF'; $I4 = C 'FFFFFF'
$W0 = C '461F0E'; $W1 = C '7C3E1D'; $W2 = C '9B704B'
$PANEL = C '2E2016'
$CLEAR = [System.Drawing.Color]::FromArgb(0, 0, 0, 0)

$script:bmp = $null
$script:g = $null
$script:rng = New-Object System.Random 7
# Branches clip to the canvas they are drawn on.
$script:clipX0 = 0; $script:clipX1 = 9999; $script:clipY0 = 0; $script:clipY1 = 9999

function NewCanvas([int]$w, [int]$h) {
    $script:bmp = New-Object System.Drawing.Bitmap $w, $h, ([System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $script:g = [System.Drawing.Graphics]::FromImage($script:bmp)
    $script:g.InterpolationMode = 'NearestNeighbor'
    $script:g.PixelOffsetMode = 'Half'
    $script:g.SmoothingMode = 'None'
    $script:g.Clear($CLEAR)
    $script:clipX0 = 0; $script:clipX1 = $w; $script:clipY0 = 0; $script:clipY1 = $h
}

function SavePng([string]$name) {
    $script:g.Dispose()
    $script:bmp.Save((Join-Path $Dest "$name.png"), [System.Drawing.Imaging.ImageFormat]::Png)
    $script:bmp.Dispose()
}

function Px([int]$x, [int]$y, $col) {
    if ($x -ge 0 -and $y -ge 0 -and $x -lt $script:bmp.Width -and $y -lt $script:bmp.Height) { $script:bmp.SetPixel($x, $y, $col) }
}

function Rect([int]$x, [int]$y, [int]$w, [int]$h, $col) {
    if ($w -le 0 -or $h -le 0) { return }
    $b = New-Object System.Drawing.SolidBrush $col
    $script:g.FillRectangle($b, $x, $y, $w, $h)
    $b.Dispose()
}

# Seed the random numbers from an element's own coordinates.
function SeedAt([int[]]$v) {
    $h = 17
    foreach ($x in $v) { $h = ($h * 31 + $x) -band 0x7fffffff }
    $script:rng = New-Object System.Random $h
}

# Grass along the foot of a picture, with a soft shade at its top edge.
function Ground([int]$x, [int]$y, [int]$w, [int]$h) {
    GrassFill $x $y $w $h ($x * 7 + $y)
    Rect $x $y $w 1 (C '000000' 60)
}

# A ground strip: dirt for a street front, else grass.
function Strip([int]$x, [int]$y, [int]$w, [int]$h, [string]$kind) {
    if ($kind -eq 'dirt') { DirtFill $x $y $w $h ($x + $y) } else { GrassFill $x $y $w $h ($x + $y) }
}

# A dirt road running top to bottom between grass verges, with ragged edges.
function RoadGround([int]$ax, [int]$ay, [int]$aw, [int]$ah, [int]$roadX, [int]$roadW) {
    GrassFill $ax $ay $aw $ah 31
    DirtFill $roadX $ay $roadW $ah 37
    for ($y = $ay; $y -lt $ay + $ah; $y++) {
        $l = @(0, 1, 2, 1, 0, 2, 3, 1)[($y * 7) % 8]
        $r = @(1, 0, 2, 3, 1, 0, 1, 2)[($y * 5) % 8]
        for ($i = 0; $i -lt $l; $i++) { Px ($roadX + $i) $y (C '6E8E3A') }
        Px ($roadX + $l) $y (C '4D4A2C')
        for ($i = 0; $i -lt $r; $i++) { Px ($roadX + $roadW - 1 - $i) $y (C '6E8E3A') }
        Px ($roadX + $roadW - 1 - $r) $y (C '4D4A2C')
    }
}


# --- original ground fills (no pack pixels): grass tufts and dirt -------------

$GR0 = C '426848'; $GR1 = C '647645'; $GR2 = C '939250'; $GR3 = C 'A8AB5C'
$DT0 = C '423734'; $DT1 = C '5B4B41'; $DT2 = C '7A6251'; $DT3 = C '897762'; $DT4 = C 'A38762'; $DT5 = C 'B2A381'

# Tuft stamps: 1 = blade, 2 = lit tip, 3 = shadow at the root.
$TUFTS = @(
    @('.2.', '121', '31.'),
    @('2.2', '111', '.3.'),
    @('.12', '13.', '3..'),
    @('21.', '.13', '..3'),
    @('.2..', '1121', '3.13'),
    @('2..', '11.', '311')
)

function GrassFill([int]$x, [int]$y, [int]$w, [int]$h, [int]$seed = 11) {
    $r = New-Object System.Random $seed
    Rect $x $y $w $h $GR1
    $n = [int]($w * $h / 11)
    for ($i = 0; $i -lt $n; $i++) {
        $st = $TUFTS[$r.Next(0, $TUFTS.Count)]
        $tx = $x + $r.Next(-2, $w); $ty = $y + $r.Next(-2, $h)
        for ($sr = 0; $sr -lt $st.Count; $sr++) {
            for ($sc = 0; $sc -lt $st[$sr].Length; $sc++) {
                $px = $tx + $sc; $py = $ty + $sr
                if ($px -lt $x -or $py -lt $y -or $px -ge $x + $w -or $py -ge $y + $h) { continue }
                $col = switch ($st[$sr][$sc]) { '1' { $GR2 } '2' { $GR3 } '3' { $GR0 } default { $null } }
                if ($col -ne $null) { Px $px $py $col }
            }
        }
    }
}

# Smooth value noise: random values on a grid, blended between corners.
function NoiseGrid([System.Random]$r, [int]$cols, [int]$rows) {
    $g = New-Object 'double[,]' ($cols + 2), ($rows + 2)
    for ($i = 0; $i -lt $cols + 2; $i++) { for ($j = 0; $j -lt $rows + 2; $j++) { $g[$i, $j] = $r.NextDouble() } }
    return , $g
}

function NoiseAt($g, [double]$fx, [double]$fy) {
    $ix = [int][Math]::Floor($fx); $iy = [int][Math]::Floor($fy)
    $tx = $fx - $ix; $ty = $fy - $iy
    $tx = $tx * $tx * (3 - 2 * $tx); $ty = $ty * $ty * (3 - 2 * $ty)
    $a = $g[$ix, $iy] + ($g[($ix + 1), $iy] - $g[$ix, $iy]) * $tx
    $b = $g[$ix, ($iy + 1)] + ($g[($ix + 1), ($iy + 1)] - $g[$ix, ($iy + 1)]) * $tx
    return $a + ($b - $a) * $ty
}

function DirtFill([int]$x, [int]$y, [int]$w, [int]$h, [int]$seed = 23) {
    $r = New-Object System.Random $seed
    $coarse = NoiseGrid $r ([int]($w / 3) + 1) ([int]($h / 2) + 1)
    $fine = NoiseGrid $r ([int]($w / 1.5) + 1) ([int]($h / 1.5) + 1)
    for ($py = 0; $py -lt $h; $py++) {
        for ($px = 0; $px -lt $w; $px++) {
            $v = 0.6 * (NoiseAt $coarse ($px / 3.0) ($py / 2.0)) + 0.4 * (NoiseAt $fine ($px / 1.5) ($py / 1.5))
            $col = $(if ($v -lt 0.2) { $DT0 } elseif ($v -lt 0.33) { $DT1 } elseif ($v -lt 0.6) { $DT2 } elseif ($v -lt 0.76) { $DT3 } else { $DT4 })
            Px ($x + $px) ($y + $py) $col
        }
    }
    for ($i = 0; $i -lt [int]($w * $h / 90); $i++) {
        $px = $x + $r.Next(0, $w - 1); $py = $y + $r.Next(0, $h - 1)
        Px $px $py $DT5; Px $px ($py + 1) $DT1
    }
}
# A post: outline, shaded body with vertical grain, end-grain cap.
function Post([int]$x, [int]$top, [int]$foot, [int]$w = 10) {
    SeedAt @($x, $top, $foot, 1)
    $h = $foot - $top
    Rect $x $top $w $h $MID
    Rect $x $top 1 $h $OUT
    Rect ($x + $w - 1) $top 1 $h $OUT
    Rect ($x + 1) $top 1 $h $DARK
    Rect ($x + $w - 2) $top 1 $h $DARK
    Rect ($x + 2) $top 1 $h $HI
    # Grain: broken vertical streaks.
    for ($i = 0; $i -lt 7; $i++) {
        $gx = $x + 3 + $rng.Next(0, $w - 5)
        $gy = $top + 4 + $rng.Next(0, [Math]::Max(1, $h - 10))
        $gl = $rng.Next(3, 9)
        for ($j = 0; $j -lt $gl -and ($gy + $j) -lt $foot; $j++) { Px $gx ($gy + $j) $DARK }
    }
    # A knot.
    $ky = $top + [int]($h * 0.55)
    Px ($x + 5) $ky $DARK; Px ($x + 6) $ky $OUT; Px ($x + 5) ($ky + 1) $OUT; Px ($x + 6) ($ky + 1) $DARK
    # Cap: end grain with a ring.
    Rect $x $top $w 1 $OUT
    Rect ($x + 1) ($top + 1) ($w - 2) 3 $HI
    Rect ($x + 2) ($top + 2) ($w - 4) 1 $HI2
    Px ($x + 4) ($top + 2) $MID; Px ($x + 5) ($top + 2) $MID
    Rect ($x + 1) ($top + 4) ($w - 2) 1 $OUT
}

# One rail column at (x, y): the 6-row plank cross-section.
function RailCol([int]$x, [int]$y, [int]$grain) {
    Px $x $y $OUT
    Px $x ($y + 1) $HI
    Px $x ($y + 2) $(if ($grain -eq 2) { $DARK } else { $MID })
    Px $x ($y + 3) $(if ($grain -eq 3) { $DARK } else { $MID })
    Px $x ($y + 4) $DARK
    Px $x ($y + 5) $OUT
}

# A rail from (x0, y0) to (x1, y1), stepping one column at a time; a jagged
# end where it snapped.
function Rail([int]$x0, [int]$y0, [int]$x1, [int]$y1, [string]$jag = '') {
    SeedAt @($x0, $y0, $x1, $y1, 2)
    $n = $x1 - $x0
    $grain = 0; $run = 0
    for ($i = 0; $i -le $n; $i++) {
        if ($run -le 0) { $grain = @(0, 0, 2, 3)[$rng.Next(0, 4)]; $run = $rng.Next(4, 12) }
        $run--
        $y = [int][Math]::Round($y0 + ($y1 - $y0) * $i / [Math]::Max(1, $n))
        RailCol ($x0 + $i) $y $grain
    }
    # Splintered end: ragged rows past the last column.
    if ($jag -ne '') {
        $ex = $(if ($jag -eq 'R') { $x1 } else { $x0 })
        $ey = $(if ($jag -eq 'R') { $y1 } else { $y0 })
        $d = $(if ($jag -eq 'R') { 1 } else { -1 })
        $len = @(2, 4, 1, 3, 1, 2)
        for ($r = 0; $r -lt 6; $r++) {
            for ($j = 1; $j -le $len[$r]; $j++) { Px ($ex + $d * $j) ($ey + $r) $(if ($r -eq 1) { $HI2 } else { $MID }) }
            Px ($ex + $d * ($len[$r] + 1)) ($ey + $r) $OUT
        }
    }
}

function NailHead([int]$x, [int]$y) {
    Px $x $y $I3; Px ($x + 1) $y $I2; Px $x ($y + 1) $I2; Px ($x + 1) ($y + 1) $I1
}


function CloseupFence([int]$ax, [int]$ay, [int]$aw, [int]$ah, [bool]$broken) {
    # Backdrop: a dim sky wash, then ground.
    Rect $ax $ay $aw $ah (C '1E2A22')
    $groundH = 12
    $foot = $ay + $ah - $groundH + 3
    Ground $ax ($ay + $ah - $groundH) $aw $groundH
    $top = $ay + 8
    $posts = @(($ax + 10), ($ax + 83), ($ax + 156))
    $rails = @(($top + 9), ($top + 22), ($top + 35))
    # Shadows on the grass.
    foreach ($p in $posts) { Rect ($p - 2) ($foot - 1) 14 2 (C '000000' 70) }
    # Left bay: whole.
    foreach ($ry in $rails) { Rail ($posts[0] + 10) $ry ($posts[1] - 1) $ry }
    if (-not $broken) {
        foreach ($ry in $rails) { Rail ($posts[1] + 10) $ry ($posts[2] - 1) $ry }
    } else {
        # Right bay: top rail holds; middle rail snapped in two; bottom rail
        # down in the grass.
        Rail ($posts[1] + 10) $rails[0] ($posts[2] - 1) $rails[0]
        Rail ($posts[1] + 10) $rails[1] ($posts[1] + 36) ($rails[1] + 7) 'R'
        Rail ($posts[2] - 30) ($rails[1] + 12) ($posts[2] - 1) $rails[1] 'L'
        Rail ($posts[1] + 14) ($foot - 6) ($posts[2] - 8) ($foot - 9)
        # Splinters in the grass.
        foreach ($s in @(@(44, -3), @(49, -2), @(60, -4))) { Px ($posts[1] + $s[0]) ($foot + $s[1]) $HI2; Px ($posts[1] + $s[0] + 1) ($foot + $s[1]) $MID }
    }
    foreach ($p in $posts) { Post $p $top $foot }
    # Nails where rails meet posts.
    foreach ($p in $posts) {
        foreach ($ry in $rails) {
            if ($p -gt $posts[0]) { NailHead ($p + 2) ($ry + 2) }
            if ($p -lt $posts[2]) { NailHead ($p + 6) ($ry + 2) }
        }
    }
    Rect $ax $ay $aw $ah (C '000000' 0)
}

# The hammer, side-on: the head hangs at the end of a level handle, its face
# (the bottom rows) toward the nail. O/I/i/h/H iron dark to light, D/W/w the
# handle's wood dark to light, '.' clear.
$HAMMER = @(
    '.OOOOO..................',
    'OhhhiIO.................',
    'OhHhiIO.................',
    'OhhhiIODDDDDDDDDDDDDDDD.',
    'OhhhiIOwwwwwwwwwwwwwwwWD',
    'OhhhiIOWWWWWWWWWWWWWWWWD',
    'OhhhiIODDDDDDDDDDDDDDDD.',
    'OhhhiIO.................',
    'OhhhiIO.................',
    'OiiiiIO.................',
    '.OOOOO..................'
)
# The grip the hammer swings about, in map space (pixel centres at +0.5): the
# middle of the handle near its end. HAMMER_* in repair_stage.gd mirror the
# frame size and where the grip lands in each frame.
$HAMMER_GRIP_X = 22.0; $HAMMER_GRIP_Y = 5.0
$HAMMER_FRAME_W = 28; $HAMMER_FRAME_H = 32
$HAMMER_FRAME_GRIP_X = 24.0; $HAMMER_FRAME_GRIP_Y = 24.0
$HAMMER_ANGLES = @(0, 20, 40, 60)

function MapColor([string]$ch) {
    switch -CaseSensitive ($ch) {
        'O' { return $I0 } 'I' { return $I1 } 'i' { return $I2 } 'h' { return $I3 } 'H' { return $I4 }
        'D' { return $W0 } 'W' { return $W1 } 'w' { return $W2 }
    }
    return $null
}

function DrawMap($rows, [int]$x, [int]$y) {
    for ($r = 0; $r -lt $rows.Count; $r++) {
        for ($c = 0; $c -lt $rows[$r].Length; $c++) {
            $col = MapColor ([string]$rows[$r][$c])
            if ($col -ne $null) { Px ($x + $c) ($y + $r) $col }
        }
    }
}

# A map turned $deg (head up) about the pivot ($px, $py) in map space, the
# pivot landing at ($ox, $oy) on the canvas, drawn into the w x h box at
# ($bx, $by). Each pixel takes the colour most of its 4 x 4 subsamples land
# on (when they cover enough of it), so a turned frame keeps clean edges.
function DrawMapTurned($rows, [double]$px, [double]$py, [double]$deg, [double]$ox, [double]$oy, [int]$bx, [int]$by, [int]$w, [int]$h) {
    $a = $deg * [Math]::PI / 180
    $ca = [Math]::Cos($a); $sa = [Math]::Sin($a)
    for ($y = $by; $y -lt $by + $h; $y++) {
        for ($x = $bx; $x -lt $bx + $w; $x++) {
            $counts = @{}
            for ($sy = 0; $sy -lt 4; $sy++) {
                for ($sx = 0; $sx -lt 4; $sx++) {
                    $dx = $x + ($sx + 0.5) / 4 - $ox
                    $dy = $y + ($sy + 0.5) / 4 - $oy
                    # Back into map space: turn by -$deg.
                    $mx = [int][Math]::Floor($dx * $ca + $dy * $sa + $px)
                    $my = [int][Math]::Floor(-$dx * $sa + $dy * $ca + $py)
                    if ($my -lt 0 -or $my -ge $rows.Count -or $mx -lt 0 -or $mx -ge $rows[$my].Length) { continue }
                    $ch = [string]$rows[$my][$mx]
                    if ($ch -ceq '.') { continue }
                    # A case-sensitive key: 'W' and 'w' are different woods.
                    $key = [string][int][char]$ch
                    $counts[$key] = 1 + $counts[$key]
                }
            }
            $best = $null; $n = 0
            foreach ($k in $counts.Keys) { if ($counts[$k] -gt $n) { $n = $counts[$k]; $best = $k } }
            if ($best -ne $null -and $n -ge 7) { Px $x $y (MapColor ([string][char][int]$best)) }
        }
    }
}

function Plank([int]$x, [int]$y, [int]$w, [int]$h) {
    SeedAt @($x, $y, $w, $h, 3)
    Rect $x $y $w $h $MID
    Rect $x $y $w 1 $OUT
    Rect $x ($y + 1) $w 1 $HI2
    Rect $x ($y + 2) $w 1 $HI
    Rect $x ($y + $h - 3) $w 2 $DARK
    Rect $x ($y + $h - 1) $w 1 $OUT
    Rect $x $y 1 $h $OUT
    Rect ($x + $w - 1) $y 1 $h $OUT
    for ($i = 0; $i -lt 26; $i++) {
        $gx = $x + 2 + $rng.Next(0, $w - 12)
        $gy = $y + 3 + $rng.Next(0, $h - 6)
        $gl = $rng.Next(4, 14)
        Rect $gx $gy $gl 1 $DARK
    }
    # Two knots.
    foreach ($kx in @(($x + 37), ($x + 101))) {
        Rect $kx ($y + 5) 4 3 $DARK; Rect ($kx + 1) ($y + 6) 2 1 $OUT
    }
}

function Nail([int]$x, [int]$boardY, [int]$h) {
    # Shaft standing proud, head on top.
    Rect ($x - 1) ($boardY - $h) 2 $h $I2
    Rect $x ($boardY - $h) 1 $h $I3
    Rect ($x - 3) ($boardY - $h - 3) 7 1 $I3
    Rect ($x - 3) ($boardY - $h - 2) 7 1 $I2
    Rect ($x - 3) ($boardY - $h - 1) 7 1 $I1
    Px ($x - 3) ($boardY - $h - 3) $I0; Px ($x + 3) ($boardY - $h - 3) $I0
    Rect ($x - 2) $boardY 5 1 (C '000000' 90)
}

function NailDriven([int]$x, [int]$boardY) {
    Rect ($x - 3) $boardY 7 2 $I2
    Rect ($x - 2) $boardY 5 1 $I3
    Rect ($x - 3) ($boardY + 2) 7 1 (C '000000' 80)
}


# Stone ramp and rope, from the bucket well sprite.
$S0 = C '2A2222'; $S1 = C '43382E'; $S2 = C '606050'; $S3 = C '787868'; $S4 = C 'A1A181'; $S5 = C 'BDBD9E'
$R0 = C 'A78548'; $R1 = C 'C3A672'; $R2 = C 'E1CDA5'
$WATER = C '24323A'
# Gold ramp (Mana Seed tool ramps) for the windlass zone.
$G0 = C '7E5E26'; $G1 = C 'C0A040'; $G2 = C 'F0E060'

$cx = 96

# The stone curb: front wall of stone courses, then the rim ring, then the
# dark opening. Drawn per pixel from two ellipses.
function Curb([int]$cy, [int]$rx, [int]$ry, [int]$rxi, [int]$ryi, [int]$wallH) {
    SeedAt @($cy, $rx, $ry, $wallH, 11)
    $vary = @{}
    for ($y = $cy - $ry - 1; $y -le $cy + $ry + $wallH + 1; $y++) {
        for ($x = $cx - $rx - 1; $x -le $cx + $rx + 1; $x++) {
            $u = ($x - $cx) / $rx
            if ([Math]::Abs($u) -gt 1) { continue }
            $low = $cy + $ry * [Math]::Sqrt(1 - $u * $u)
            # Front wall.
            if ($y -gt $low -and $y -le $low + $wallH) {
                $d = $y - $low
                $row = [int][Math]::Floor($d / 6)
                $bx = $x + $row * 7
                $key = "$row/$([int][Math]::Floor($bx / 13))"
                if (-not $vary.ContainsKey($key)) { $vary[$key] = $rng.Next(0, 3) }
                $col = $S3
                $au = [Math]::Abs($u)
                if ($au -gt 0.82) { $col = $S2 } elseif ($au -lt 0.4 -and $vary[$key] -gt 0) { $col = $S4 } elseif ($vary[$key] -eq 2) { $col = $S4 }
                if ($u -gt 0.55) { $col = $(if ($col -eq $S4) { $S3 } else { $S2 }) }
                if (([int][Math]::Floor($d)) % 6 -eq 0 -or ($bx % 13) -eq 0) { $col = $S1 }
                if (([int][Math]::Floor($d)) % 6 -eq 1 -and ($bx % 13) -ne 0 -and $col -ne $S2) { $col = $S5 }
                if ($y -ge $low + $wallH - 0.5 -or $au -gt 0.985) { $col = $S0 }
                Px $x $y $col
            }
        }
    }
    for ($y = $cy - $ry - 1; $y -le $cy + $ry + 1; $y++) {
        for ($x = $cx - $rx - 1; $x -le $cx + $rx + 1; $x++) {
            $dx = $x - $cx; $dy = $y - $cy
            $o = ($dx / $rx) * ($dx / $rx) + ($dy / $ry) * ($dy / $ry)
            $i = ($dx / $rxi) * ($dx / $rxi) + ($dy / $ryi) * ($dy / $ryi)
            if ($o -gt 1) { continue }
            if ($i -le 1) {
                # The opening: dark, a dull glint of water low in it.
                $col = $S0
                if ($dy -gt $ryi * 0.25 -and $i -lt 0.7) { $col = $WATER }
                if ($i -gt 0.82) { $col = $S0 }
                Px $x $y $col
                continue
            }
            $ang = [Math]::Atan2($dy * $rx / $ry, $dx)
            $seg = ($ang + [Math]::PI) / (2 * [Math]::PI) * 16
            $frac = $seg - [Math]::Floor($seg)
            $col = $S4
            if ($dy -lt 0) { $col = $S3 }
            if ($i -lt 1.25) { $col = $(if ($dy -lt 0) { $S2 } else { $S5 }) }
            if ($frac -lt 0.07) { $col = $S1 }
            if ($o -gt 0.9) { $col = $S0 }
            if ($i -lt 1.08) { $col = $S0 }
            Px $x $y $col
        }
    }
}

# A wooden beam lying at any slope: rows outline/highlight/body/shadow.
function Beam([int]$x0, [int]$y0, [int]$x1, [int]$y1, [int]$h, [string]$jag = '') {
    $n = $x1 - $x0
    $grain = @{}
    for ($i = 0; $i -le $n; $i++) {
        $y = [int][Math]::Round($y0 + ($y1 - $y0) * $i / [Math]::Max(1, $n))
        for ($r = 0; $r -lt $h; $r++) {
            $col = $MID
            if ($r -eq 0 -or $r -eq $h - 1) { $col = $OUT }
            elseif ($r -eq 1) { $col = $HI }
            elseif ($r -ge $h - 3) { $col = $DARK }
            elseif ((($i + $r * 5) % 11) -eq 0 -or (($i * 3 + $r) % 17) -eq 0) { $col = $DARK }
            Px ($x0 + $i) ($y + $r) $col
        }
    }
    if ($jag -ne '') {
        $ex = $(if ($jag -eq 'R') { $x1 } else { $x0 })
        $ey = [int]$(if ($jag -eq 'R') { $y1 } else { $y0 })
        $d = $(if ($jag -eq 'R') { 1 } else { -1 })
        for ($r = 0; $r -lt $h; $r++) {
            $len = @(1, 3, 2, 4, 1, 2, 3, 1)[$r % 8]
            for ($j = 1; $j -le $len; $j++) { Px ($ex + $d * $j) ($ey + $r) $(if ($r -eq 1) { $HI2 } else { $MID }) }
            Px ($ex + $d * ($len + 1)) ($ey + $r) $OUT
        }
    }
}

# A post standing upright, with a jagged top when snapped.
function WellPost([int]$x, [int]$top, [int]$foot, [bool]$snapped) {
    Post $x $top $foot 8
    if ($snapped) {
        $teeth = @(0, 3, 1, 4, 2, 5, 1, 0)
        for ($c = 0; $c -lt 8; $c++) {
            for ($r = 0; $r -lt 5; $r++) { Px ($x + $c) ($top + $r) (C '1E2A22') }
            for ($r = 5 - $teeth[$c]; $r -lt 5; $r++) { Px ($x + $c) ($top + $r) $(if ($c -gt 0 -and $c -lt 7) { $HI2 } else { $OUT }) }
            Px ($x + $c) ($top + 4 - $teeth[$c]) $OUT
        }
    }
}

# The drum: a horizontal log with rope wound round it.
function Drum([int]$x, [int]$y, [int]$w, [int]$h, [int]$ropeFrom, [int]$ropeTo) {
    Beam $x $y ($x + $w) $y $h
    for ($i = $ropeFrom; $i -lt $ropeTo; $i++) {
        for ($r = 1; $r -lt $h - 1; $r++) {
            $p = ($i + $r) % 4
            $col = $(if ($p -eq 0) { $R0 } elseif ($p -eq 3) { $R2 } else { $R1 })
            Px ($x + $i) ($y + $r) $col
        }
    }
    Rect ($x + $ropeFrom) $y ($ropeTo - $ropeFrom) 1 $OUT
    Rect ($x + $ropeFrom) ($y + $h - 1) ($ropeTo - $ropeFrom) 1 $OUT
    # End grain discs.
    foreach ($ex in @($x, ($x + $w))) { Rect ($ex - 1) ($y + 1) 3 ($h - 2) $HI; Px $ex ($y + [int]($h / 2)) $MID }
}

function Rope([int]$x, [int]$y0, [int]$y1) {
    for ($y = $y0; $y -le $y1; $y++) {
        Px $x $y $(if ($y % 3 -eq 0) { $R0 } else { $R1 })
        Px ($x + 1) $y $(if ($y % 3 -eq 1) { $R2 } else { $R0 })
    }
}

# A bucket: tapered staves, two iron hoops, a bail.
function Bucket([int]$x, [int]$y, [bool]$onSide) {
    if (-not $onSide) {
        for ($r = 0; $r -lt 12; $r++) {
            $inset = [int][Math]::Floor($r / 6)
            $w = 12 - 2 * $inset
            for ($c = 0; $c -lt $w; $c++) {
                $col = $(if ($c % 3 -eq 0) { $DARK } elseif ($c -eq 1) { $HI } else { $MID })
                if ($r -eq 2 -or $r -eq 8) { $col = $(if ($c -lt 3) { $I3 } else { $I2 }) }
                if ($c -eq 0 -or $c -eq $w - 1 -or $r -eq 11) { $col = $OUT }
                Px ($x + $inset + $c) ($y + $r) $col
            }
        }
        Rect $x $y 12 1 $OUT
        Rect ($x + 1) ($y + 1) 10 1 $S0
        # Bail.
        for ($c = 1; $c -lt 11; $c++) { $by = $y - 4 + [int][Math]::Round(([Math]::Pow(($c - 5.5) / 5.5, 2)) * 4); Px ($x + $c) $by $I1 }
    } else {
        for ($c = 0; $c -lt 12; $c++) {
            $inset = [int][Math]::Floor($c / 6)
            $h = 12 - 2 * $inset
            for ($r = 0; $r -lt $h; $r++) {
                $col = $(if ($r % 3 -eq 0) { $DARK } elseif ($r -eq 1) { $HI } else { $MID })
                if ($c -eq 2 -or $c -eq 8) { $col = $(if ($r -lt 3) { $I3 } else { $I2 }) }
                if ($r -eq 0 -or $r -eq $h - 1 -or $c -eq 11) { $col = $OUT }
                Px ($x + $c) ($y + $inset + $r) $col
            }
        }
        # Mouth facing left: dark ellipse.
        Rect ($x - 1) ($y + 1) 2 10 $OUT
        Rect $x ($y + 2) 1 8 $S0
    }
}

function Crank([int]$x, [int]$y) {
    # Iron arm out from the drum's end, then the wooden grip.
    Rect $x $y 3 2 $I2; Rect $x ($y + 2) 3 1 $I1
    Rect ($x + 2) ($y - 9) 2 11 $I2; Rect ($x + 2) ($y - 9) 1 11 $I3
    Rect ($x + 4) ($y - 10) 5 3 $W1; Rect ($x + 4) ($y - 10) 5 1 $W2; Rect ($x + 8) ($y - 10) 1 3 $W0
}

function WellCloseup([int]$ax, [int]$ay, [int]$aw, [int]$ah, [bool]$broken) {
    Rect $ax $ay $aw $ah (C '1E2A22')
    $groundH = 30
    Ground $ax ($ay + $ah - $groundH) $aw $groundH
    $cy = $ay + 58
    $foot = $cy + 4
    $lx = $cx - 50; $rxp = $cx + 42
    $beamY = $ay + 7
    if (-not $broken) {
        # Posts behind the curb, frame above.
        WellPost $lx ($beamY + 3) $foot $false
        WellPost $rxp ($beamY + 3) $foot $false
        Curb $cy 52 15 37 10 22
        Beam ($lx - 4) $beamY ($rxp + 12) $beamY 7
        Drum ($lx + 8) ($beamY + 22) ($rxp - $lx - 8) 10 30 58
        Crank ($rxp + 8) ($beamY + 26)
        Rope ($cx - 1) ($beamY + 32) ($cy - 6)
        Bucket ($cx - 6) ($cy - 6) $false
    } else {
        WellPost $lx ($beamY + 3) $foot $false
        WellPost $rxp ($cy - 20) $foot $true
        Curb $cy 52 15 37 10 22
        # The beam fell: one end on the rim, the other in the grass.
        Beam ($lx + 10) ($cy - 12) ($rxp + 14) ($cy + 22) 7
        # The snapped top of the right post, down in the grass.
        Beam ($rxp - 34) ($ay + $ah - 13) ($rxp - 2) ($ay + $ah - 15) 8 'L'
        # The drum rolled off; slack rope across the rim.
        Drum ($ax + 6) ($ay + $ah - 18) 34 10 10 22
        for ($i = 0; $i -lt 40; $i++) { $ry = $cy - 13 + [int]([Math]::Sin($i / 5.0) * 2) + [int]($i / 6); Px ($lx + 16 + $i) $ry $R1; Px ($lx + 16 + $i) ($ry + 1) $R0 }
        Bucket ($cx + 44) ($ay + $ah - 16) $true
        foreach ($s in @(@(-10, -6), @(-6, -4), @(4, -7))) { Px ($rxp + $s[0]) ($ay + $ah + $s[1]) $HI2; Px ($rxp + $s[0] + 1) ($ay + $ah + $s[1]) $MID }
    }
}

# --- the windlass game, at the panel's resolution ---------------------------

function Wheel([int]$wx, [int]$wy, [double]$turn) {
    for ($y = -11; $y -le 11; $y++) {
        for ($x = -11; $x -le 11; $x++) {
            $d = [Math]::Sqrt($x * $x + $y * $y)
            if ($d -le 10.6 -and $d -ge 7.6) {
                $col = $MID
                if ($d -ge 9.8) { $col = $OUT } elseif ($d -le 8.4) { $col = $DARK } elseif ($y -lt 0) { $col = $HI }
                Px ($wx + $x) ($wy + $y) $col
            }
        }
    }
    for ($s = 0; $s -lt 4; $s++) {
        $a = $turn + $s * [Math]::PI / 2
        for ($t = 2; $t -le 8; $t++) {
            $px = [int][Math]::Round($wx + [Math]::Cos($a) * $t)
            $py = [int][Math]::Round($wy + [Math]::Sin($a) * $t)
            Px $px $py $MID; Px ($px + 1) $py $HI
        }
        # A handle peg on one spoke.
        if ($s -eq 0) { $hx = [int][Math]::Round($wx + [Math]::Cos($a) * 9); $hy = [int][Math]::Round($wy + [Math]::Sin($a) * 9); Rect ($hx - 1) ($hy - 1) 3 3 $W1; Px ($hx - 1) ($hy - 1) $W2 }
    }
    Rect ($wx - 2) ($wy - 2) 5 5 $I1; Rect ($wx - 1) ($wy - 1) 3 3 $I2; Px ($wx - 1) ($wy - 1) $I3
}

function Peg([int]$x, [int]$y) {
    # An iron pin with a ring head, riding the bar.
    Rect ($x - 1) ($y - 6) 3 18 $I0
    Rect $x ($y - 5) 1 16 $I3
    Rect ($x - 2) ($y - 10) 5 5 $I0; Rect ($x - 1) ($y - 9) 3 3 $I2; Px $x ($y - 8) $I0; Px ($x - 1) ($y - 9) $I3
}



# Bark ramp, from the fallen chestnut.
$B0 = C '292129'; $B1 = C '442B28'; $B2 = C '67453E'; $B3 = C '80594F'; $B4 = C '937267'; $B5 = C 'AF9485'
# Cut wood (end grain).
$E0 = C 'A07030'; $E1 = C 'C8934A'; $E2 = C 'E0B878'; $E3 = C 'F0D29A'
$G0 = C '7E5E26'; $G1 = C 'C0A040'; $G2 = C 'F0E060'

# Ground: grass verges either side of a dirt road running top to bottom.
function LogBody([int]$x0, [int]$x1, [int]$y, [int]$h) {
    SeedAt @($x0, $x1, $y, $h, 9)
    for ($x = $x0; $x -le $x1; $x++) {

        for ($r = 0; $r -lt $h; $r++) {
            $t = $r / ($h - 1)
            $col = $B2
            if ($t -lt 0.2) { $col = $B4 } elseif ($t -lt 0.4) { $col = $B3 } elseif ($t -gt 0.8) { $col = $B1 }
            if ($r -eq 1 -and ($x % 5) -ne 0) { $col = $B5 }

            if ($r -eq 0 -or $r -eq $h - 1) { $col = $B0 }
            Px $x ($y + $r) $col
        }
    }
    # Fissures running along the log.
    for ($r = 3; $r -lt $h - 2; $r += 2) {
        $x = $x0 + $rng.Next(0, 6)
        while ($x -lt $x1 - 2) {
            $len = $rng.Next(4, 12)
            $col = $(if ($r -lt $h / 2) { $B2 } else { $B1 })
            for ($i = 0; $i -lt $len -and $x + $i -lt $x1 - 1; $i++) { Px ($x + $i) ($y + $r) $col }
            if ($r -lt $h / 2) { for ($i = 1; $i -lt $len - 1 -and $x + $i -lt $x1 - 1; $i++) { Px ($x + $i) ($y + $r - 1) $B4 } }
            $x += $len + $rng.Next(3, 10)
        }
    }
    # Shadow on the ground.
    Rect ($x0 + 2) ($y + $h) ($x1 - $x0 - 2) 2 (C '000000' 80)
}

# A sawn face: an upright ellipse of end grain with rings, bark round it.
function CutFace([int]$x, [int]$y, [int]$h) {
    $ry = ($h - 1) / 2.0; $rx = [Math]::Max(3.0, $h / 3.5)
    $cy = $y + $ry
    for ($yy = $y; $yy -lt $y + $h; $yy++) {
        for ($xx = [int]($x - $rx); $xx -le [int]($x + $rx); $xx++) {
            $d = [Math]::Sqrt([Math]::Pow(($xx - $x) / $rx, 2) + [Math]::Pow(($yy - $cy) / $ry, 2))
            if ($d -gt 1.0) { continue }
            $col = $E2
            if ($d -gt 0.86) { $col = $B0 } elseif ($d -gt 0.74) { $col = $B2 }
            elseif ([Math]::Abs($d - 0.55) -lt 0.06 -or [Math]::Abs($d - 0.3) -lt 0.06) { $col = $E1 }
            elseif ($d -lt 0.12) { $col = $E0 }
            elseif ($yy -lt $cy - $ry * 0.3 -and $xx -lt $x) { $col = $E3 }
            Px $xx $yy $col
        }
    }
}

# Bare branches fanning out from the crown end.
function Branch([int]$x0, [int]$y0, [int]$x1, [int]$y1, [int]$w) {
    $n = [Math]::Max([Math]::Abs($x1 - $x0), [Math]::Abs($y1 - $y0))
    for ($i = 0; $i -le $n; $i++) {
        $x = [int][Math]::Round($x0 + ($x1 - $x0) * $i / $n)
        $y = [int][Math]::Round($y0 + ($y1 - $y0) * $i / $n)
        $ww = [Math]::Max(1, [int][Math]::Round($w * (1 - $i / ($n + 1.0))))
        if ($x -lt $script:clipX0 -or $x -ge $script:clipX1) { continue }
        for ($k = 0; $k -lt $ww; $k++) {
            if (($y + $k) -lt $script:clipY0 -or ($y + $k) -ge $script:clipY1) { continue }
            Px $x ($y + $k) $(if ($k -eq 0) { $B4 } elseif ($k -eq $ww - 1) { $B0 } else { $B2 })
        }
        if ($ww -eq 1 -and $y -ge $script:clipY0 -and $y -lt $script:clipY1) { Px $x $y $B3 }
    }
}

function Limb([double]$x, [double]$y, [double]$ang, [double]$len, [int]$w, [int]$depth) {
    $x1 = $x + [Math]::Cos($ang) * $len
    $y1 = $y + [Math]::Sin($ang) * $len
    Branch ([int]$x) ([int]$y) ([int]$x1) ([int]$y1) $w
    if ($depth -le 0) { return }
    $kids = $rng.Next(2, 4)
    for ($i = 0; $i -lt $kids; $i++) {
        $t = 0.45 + 0.5 * $rng.NextDouble()
        $bx = $x + ($x1 - $x) * $t; $by = $y + ($y1 - $y) * $t
        $spread = (0.35 + 0.4 * $rng.NextDouble()) * $(if ($i % 2 -eq 0) { 1 } else { -1 })
        Limb $bx $by ($ang + $spread) ($len * (0.5 + 0.2 * $rng.NextDouble())) ([Math]::Max(1, $w - 2)) ($depth - 1)
    }
}

function Crown([int]$x, [int]$y, [int]$h) {
    SeedAt @($x, $y, $h, 10)
    $mid = $y + [int]($h / 2)
    Limb $x ($y + 3) ([Math]::PI + 0.55) 34 8 3
    Limb $x ($mid) ([Math]::PI + 0.05) 44 9 3
    Limb $x ($y + $h - 6) ([Math]::PI - 0.5) 34 8 3
}
function Stump([int]$x, [int]$y) {
    # Bark sides, then a splintered top.
    for ($c = 0; $c -lt 16; $c++) {
        $t = $c / 15.0
        for ($r = 0; $r -lt 14; $r++) {
            $col = $(if ($t -lt 0.25) { $B4 } elseif ($t -lt 0.5) { $B3 } elseif ($t -gt 0.8) { $B1 } else { $B2 })
            if (($c % 4) -eq 2 -and $r -gt 2) { $col = $B1 }
            if ($c -eq 0 -or $c -eq 15 -or $r -eq 13) { $col = $B0 }
            Px ($x + $c) ($y + $r) $col
        }
    }
    $teeth = @(2, 5, 3, 7, 4, 6, 2, 8, 5, 3, 6, 4, 2, 5, 3, 1)
    for ($c = 0; $c -lt 16; $c++) {
        for ($r = 1; $r -le $teeth[$c]; $r++) { Px ($x + $c) ($y - $r) $(if ($r -eq $teeth[$c]) { $B0 } elseif ($c -gt 1 -and $c -lt 14) { $E2 } else { $B3 }) }
    }
    Rect ($x + 1) $y 14 2 $E1
    Rect ($x - 2) ($y + 13) 20 2 (C '000000' 80)
}

function RoadCloseup([int]$ax, [int]$ay, [int]$aw, [int]$ah, [int]$cutTo, [bool]$cleared) {
    $roadX = $ax + 56; $roadW = 64
    $script:clipX0 = $ax; $script:clipX1 = $ax + $aw; $script:clipY0 = $ay; $script:clipY1 = $ay + $ah
    RoadGround $ax $ay $aw $ah $roadX $roadW
    $logY = $ay + 34; $logH = 16
    Stump ($ax + 148) ($logY - 2)
    if (-not $cleared) {
        Crown ($ax + 40) $logY $logH
        LogBody ($ax + 30) $cutTo $logY $logH
        CutFace $cutTo $logY $logH
    } else {
        # The crown dragged onto the left verge as a brush pile.
        Limb ($ax + 46) ($ay + 66) ([Math]::PI + 0.2) 28 6 2
        Limb ($ax + 44) ($ay + 72) ([Math]::PI - 0.25) 30 6 2
        Limb ($ax + 40) ($ay + 60) ([Math]::PI + 0.6) 22 5 2
    }
    # Sawn rounds stacked on the right verge, as many as are cut.
    $rounds = $(if ($cleared) { 4 } else { 2 })
    $sx = $ax + 130; $sy = $ay + $ah - 20
    for ($i = 0; $i -lt $rounds; $i++) {
        $px = $sx + ($i % 2) * 16 + $(if ($i -ge 2) { 8 } else { 0 })
        $py = $sy - $(if ($i -ge 2) { 10 } else { 0 })
        LogBody ($px - 6) ($px + 4) $py 12
        CutFace ($px - 6) $py 12
    }
    # Sawdust where the cuts were made.
    if (-not $cleared) { foreach ($d in @(@(0, 2), @(3, 4), @(-2, 5), @(5, 1), @(1, 7), @(-4, 3))) { Px ($cutTo + $d[0]) ($logY + $logH + $d[1]) $E2 } }
}

function LogBack([int]$fx, [int]$fy, [int]$r, [int]$steps) {
    $inside = {
        param($px, $py)
        for ($t = 1; $t -le $steps; $t++) {
            $dx = $px - ($fx + $t); $dy = $py - ($fy - [int]($t * 0.6))
            if ($dx * $dx + $dy * $dy -le $r * $r) { return $true }
        }
        return $false
    }
    for ($py = $fy - $r - $steps; $py -le $fy + $r; $py++) {
        for ($px = $fx - $r; $px -le $fx + $r + $steps + 1; $px++) {
            if (-not (& $inside $px $py)) { continue }
            $edge = -not ((& $inside ($px + 1) $py) -and (& $inside ($px - 1) $py) -and (& $inside $px ($py + 1)) -and (& $inside $px ($py - 1)))
            $rel = ($py - $fy) / $r
            $col = $(if ($edge) { $B0 } elseif ($rel -lt -0.55) { $B4 } elseif ($rel -lt -0.2) { $B3 } elseif ($rel -gt 0.5) { $B1 } else { $B2 })
            if (-not $edge -and (($px * 3 + $py) % 9) -eq 0) { $col = $B1 }
            Px $px $py $col
        }
    }
}

# A round of log seen end-on: bark ring, rings of wood, pith. $back draws the
# bark of the receding body only.
function Disc([int]$x, [int]$y, [int]$r, [bool]$back) {
    for ($yy = -$r; $yy -le $r; $yy++) {
        for ($xx = -$r; $xx -le $r; $xx++) {
            $d = [Math]::Sqrt($xx * $xx + $yy * $yy) / $r
            if ($d -gt 1.0) { continue }
            if ($back) {
                $col = $(if ($d -gt 0.93) { $B0 } elseif ($yy -lt -$r * 0.4) { $B4 } elseif ($xx -gt $r * 0.3) { $B1 } else { $B2 })
                Px ($x + $xx) ($y + $yy) $col
                continue
            }
            $col = $E2
            if ($d -gt 0.93) { $col = $B0 }
            elseif ($d -gt 0.8) { $col = $(if ($yy -lt 0) { $B3 } else { $B2 }) }
            elseif ([Math]::Abs($d - 0.62) -lt 0.035 -or [Math]::Abs($d - 0.42) -lt 0.035 -or [Math]::Abs($d - 0.22) -lt 0.035) { $col = $E1 }
            elseif ($d -lt 0.07) { $col = $E0 }
            elseif ($yy -lt -$r * 0.35 -and $xx -lt 0 -and $d -gt 0.45) { $col = $E3 }
            # A check crack from the pith outward.
            if ($xx -gt 0 -and [Math]::Abs($yy - [int]($xx * 0.3)) -lt 1 -and $d -lt 0.7 -and $d -gt 0.1) { $col = $E0 }
            Px ($x + $xx) ($y + $yy) $col
        }
    }
}

# A crosscut blade lying in the plane of the screen: wooden grip, iron back,
# teeth along the lower edge.
function SawBlade([int]$x, [int]$y, [int]$len) {
    Rect $x ($y - 2) 7 10 $W1; Rect $x ($y - 2) 7 1 $W2; Rect $x ($y - 2) 1 10 $W0; Rect ($x + 6) ($y - 2) 1 10 $W0; Rect $x ($y + 7) 7 1 $W0
    for ($hy = 1; $hy -le 4; $hy++) { for ($hx = 2; $hx -le 4; $hx++) { Px ($x + $hx) ($y + $hy) $CLEAR } }
    $bx = $x + 7
    Rect $bx $y ($len - 7) 1 $I0
    Rect $bx ($y + 1) ($len - 7) 1 $I3
    Rect $bx ($y + 2) ($len - 7) 2 $I2
    Rect $bx ($y + 4) ($len - 7) 1 $I1
    for ($i = 0; $i -lt $len - 7; $i++) {
        Px ($bx + $i) ($y + 5) $(if ($i % 2 -eq 0) { $I1 } else { $I0 })
        if ($i % 2 -eq 0) { Px ($bx + $i) ($y + 6) $I0 }
    }
    Rect ($bx + $len - 8) $y 1 6 $I0
}

# Plaster, shingle and stone ramps for a half-timbered shop front.
$PL0 = C 'A08C65'; $PL1 = C 'C9B48C'; $PL2 = C 'E3D3AE'; $PL3 = C 'F0E4C6'
$SH0 = C '3A3046'; $SH1 = C '5A4E6B'; $SH2 = C '7E7090'; $SH3 = C 'A497B4'
$S0 = C '2A2222'; $S1 = C '43382E'; $S2 = C '606050'; $S3 = C '787868'; $S4 = C 'A1A181'
$E1 = C 'C8934A'; $E2 = C 'E0B878'
$STRAW0 = C 'A78548'; $STRAW1 = C 'D9BE6E'
function Timber([int]$x, [int]$y, [int]$w, [int]$h) {
    SeedAt @($x, $y, $w, $h, 4)
    Rect $x $y $w $h $DARK
    Rect $x $y $w 1 $OUT; Rect $x ($y + $h - 1) $w 1 $OUT
    Rect $x $y 1 $h $OUT; Rect ($x + $w - 1) $y 1 $h $OUT
    if ($w -gt $h) { Rect ($x + 1) ($y + 1) ($w - 2) 1 $MID } else { Rect ($x + 1) ($y + 1) 1 ($h - 2) $MID }
    for ($i = 0; $i -lt [int](($w * $h) / 40); $i++) {
        $gx = $x + 2 + $rng.Next(0, [Math]::Max(1, $w - 4)); $gy = $y + 2 + $rng.Next(0, [Math]::Max(1, $h - 4))
        if ($w -gt $h) { Rect $gx $gy ([Math]::Min(5, $x + $w - 1 - $gx)) 1 $OUT } else { Rect $gx $gy 1 ([Math]::Min(5, $y + $h - 1 - $gy)) $OUT }
    }
}

function Plaster([int]$x, [int]$y, [int]$w, [int]$h) {
    SeedAt @($x, $y, $w, $h, 5)
    Rect $x $y $w $h $PL2
    Rect $x $y $w 2 $PL1
    Rect $x $y 1 $h $PL1
    for ($i = 0; $i -lt [int](($w * $h) / 30); $i++) {
        $px = $x + 1 + $rng.Next(0, $w - 2); $py = $y + 2 + $rng.Next(0, $h - 3)
        Px $px $py $(if ($rng.Next(0, 3) -eq 0) { $PL3 } else { $PL1 })
    }
}

# A plank door with iron strap hinges and a ring pull.
function Door([int]$x, [int]$y, [int]$w, [int]$h) {
    SeedAt @($x, $y, $w, $h, 6)
    for ($b = 0; $b -lt $w; $b += 6) {
        $bw = [Math]::Min(6, $w - $b)
        Rect ($x + $b) $y $bw $h $MID
        Rect ($x + $b) $y 1 $h $OUT
        Rect ($x + $b + 1) $y 1 $h $HI
        for ($i = 0; $i -lt 4; $i++) { Rect ($x + $b + 2 + $rng.Next(0, [Math]::Max(1, $bw - 3))) ($y + 2 + $rng.Next(0, $h - 8)) 1 ($rng.Next(3, 7)) $DARK }
    }
    Rect ($x + $w - 1) $y 1 $h $OUT
    Rect $x $y $w 1 $OUT
    foreach ($hy in @(($y + 6), ($y + $h - 9))) { Rect ($x + 1) $hy ($w - 8) 3 $I1; Rect ($x + 1) $hy ($w - 8) 1 $I3; Px ($x + 3) ($hy + 1) $I0; Px ($x + 9) ($hy + 1) $I0 }
    $rx = $x + $w - 6; $ry = $y + [int]($h / 2)
    Px $rx $ry $I3; Px ($rx - 1) ($ry + 1) $I2; Px ($rx + 1) ($ry + 1) $I2; Px ($rx - 1) ($ry + 2) $I1; Px ($rx + 1) ($ry + 2) $I1; Px $rx ($ry + 3) $I1
}

# A shutter of three boards and a batten; $tilt hangs it from its top hinge.
function Shutter([int]$x, [int]$y, [int]$w, [int]$h, [double]$tilt) {
    for ($c = 0; $c -lt $w; $c++) {
        $dy = [int][Math]::Round($c * $tilt)
        for ($r = 0; $r -lt $h; $r++) {
            $col = $MID
            if (($c % 5) -eq 0) { $col = $OUT } elseif (($c % 5) -eq 1) { $col = $HI }
            if ($r -ge [int]($h / 2) - 1 -and $r -le [int]($h / 2) + 1) { $col = $(if ($r -eq [int]($h / 2) - 1) { $HI2 } else { $DARK }) }
            if ($r -eq 0 -or $r -eq $h - 1 -or $c -eq $w - 1) { $col = $OUT }
            Px ($x + $c) ($y + $r + $dy) $col
        }
    }
}

function Window([int]$x, [int]$y, [int]$w, [int]$h, [bool]$hanging) {
    Rect ($x - 1) ($y - 1) ($w + 2) ($h + 2) $OUT
    Rect $x $y $w $h (C '1C1A22')
    Rect ($x + [int]($w / 2)) $y 1 $h $DARK; Rect $x ($y + [int]($h / 2)) $w 1 $DARK
    Rect ($x + 1) ($y + 1) 3 2 (C '4A5868'); Rect ($x + [int]($w / 2) + 2) ($y + 1) 3 2 (C '4A5868')
    Timber ($x - 3) ($y + $h + 1) ($w + 6) 4
    Shutter ($x - 12) $y 11 $h 0
    if ($hanging) {
        Shutter ($x + $w + 1) ($y + 2) 11 $h 0.55
    } else {
        Shutter ($x + $w + 1) $y 11 $h 0
    }
}

# The debris heap: broken boards criss-crossed, slipped shingles, splinters.
function Shingle([int]$x, [int]$y) {
    Rect $x $y 8 4 $SH1; Rect $x $y 8 1 $SH3; Rect ($x + 1) ($y + 1) 6 1 $SH2; Rect $x ($y + 3) 8 1 $SH0
    Px $x $y $SH0; Px ($x + 7) $y $SH0
}

function Board([int]$x0, [int]$y0, [int]$x1, [int]$y1, [int]$h) {
    $n = [Math]::Max(1, $x1 - $x0)
    for ($i = 0; $i -le $n; $i++) {
        $y = [int][Math]::Round($y0 + ($y1 - $y0) * $i / $n)
        for ($r = 0; $r -lt $h; $r++) {
            $col = $(if ($r -eq 0 -or $r -eq $h - 1) { $OUT } elseif ($r -eq 1) { $HI } elseif ($r -eq $h - 2) { $DARK } else { $MID })
            if ($r -gt 1 -and $r -lt $h - 2 -and (($i * 3 + $r * 7) % 13) -eq 0) { $col = $DARK }
            Px ($x0 + $i) ($y + $r) $col
        }
    }
    # Splintered end.
    $len = @(1, 3, 2, 4, 1)
    for ($r = 0; $r -lt $h; $r++) { for ($j = 1; $j -le $len[$r % 5]; $j++) { Px ($x1 + $j) ($y1 + $r) $(if ($r -eq 1) { $HI2 } else { $MID }) }; Px ($x1 + $len[$r % 5] + 1) ($y1 + $r) $OUT }
}

function Heap([int]$x, [int]$y) {
    Rect ($x - 8) ($y + 16) 84 3 (C '000000' 80)
    # A board leaning against the wall, others criss-crossed at its foot.
    Board ($x + 8) ($y + 14) ($x + 26) ($y - 22) 6
    Board ($x - 6) ($y + 12) ($x + 46) ($y - 4) 7
    Shingle ($x + 2) ($y + 4); Shingle ($x + 10) ($y + 9); Shingle ($x + 34) ($y + 1); Shingle ($x + 52) ($y + 8)
    Board ($x + 14) ($y + 2) ($x + 66) ($y + 12) 7
    Shingle ($x + 40) ($y + 10); Shingle ($x + 24) ($y + 12); Shingle ($x + 60) ($y + 13)
    Board ($x) ($y + 15) ($x + 38) ($y + 11) 5
    foreach ($s in @(@(72, 15), @(76, 13), @(-9, 14), @(48, 17), @(30, 17), @(80, 16))) { Px ($x + $s[0]) ($y + $s[1]) $HI2; Px ($x + $s[0] + 1) ($y + $s[1]) $MID }
}

function ShopCloseup([int]$ax, [int]$ay, [int]$aw, [int]$ah, [bool]$broken) {
    $groundY = $ay + $ah - 14
    # The wall: plaster panels in a timber frame on a stone footing.
    Plaster $ax $ay $aw ($groundY - $ay)
    Timber $ax ($ay + 4) $aw 6
    foreach ($px in @(($ax + 2), ($ax + 48), ($ax + 100), ($ax + 166))) { Timber $px ($ay + 4) 8 ($groundY - $ay - 4) }
    # A brace in the right bay.
    for ($i = 0; $i -lt 56; $i++) { $by = $ay + 10 + [int]($i * 0.86); Rect ($ax + 108 + $i) $by 5 1 $DARK; Px ($ax + 108 + $i) $by $OUT; Px ($ax + 112 + $i) $by $MID }
    for ($sx = $ax; $sx -lt $ax + $aw; $sx += 11) { Rect $sx ($groundY - 6) 11 6 $S3; Rect $sx ($groundY - 6) 11 1 $S4; Rect $sx ($groundY - 6) 1 6 $S1; Rect $sx ($groundY - 1) 11 1 $S1 }
    Door ($ax + 62) ($ay + 16) 26 ($groundY - $ay - 22)
    Timber ($ax + 59) ($ay + 12) 32 4
    Window ($ax + 124) ($ay + 20) 22 18 $broken
    # A hanging sign over the door bay.
    Rect ($ax + 18) ($ay + 14) 1 6 $I1; Rect ($ax + 34) ($ay + 14) 1 6 $I1
    Timber ($ax + 14) ($ay + 20) 24 12
    Rect ($ax + 19) ($ay + 24) 14 1 $HI2; Rect ($ax + 21) ($ay + 27) 10 1 $HI2
    Strip $ax $groundY $aw 14 'dirt'
    if ($broken) {
        # A crack in the plaster and the heap before the door.
        $cx = $ax + 30; $cy = $ay + 36
        foreach ($d in @(@(0, 0), @(1, 1), @(1, 2), @(2, 3), @(2, 4), @(1, 5), @(2, 6), @(3, 7), @(4, 7))) { Px ($cx + $d[0]) ($cy + $d[1]) $PL0 }
        Heap ($ax + 56) ($groundY - 6)
    }
}

# --- crate --------------------------------------------------------------------

function CrateBox([int]$x, [int]$y, [int]$w, [int]$h, [int]$topH) {
    SeedAt @($x, $y, $w, $h, 7)
    # Front face: horizontal slats between corner battens.
    for ($r = 0; $r -lt $h; $r++) {
        $slat = $r % 7
        $col = $(if ($slat -eq 0) { $OUT } elseif ($slat -eq 1) { $HI } elseif ($slat -eq 6) { $DARK } else { $MID })
        Rect $x ($y + $topH + $r) $w 1 $col
    }
    for ($i = 0; $i -lt 18; $i++) { Rect ($x + 6 + $rng.Next(0, $w - 16)) ($y + $topH + 2 + 7 * $rng.Next(0, [int]($h / 7)) + 2) ($rng.Next(4, 9)) 1 $DARK }
    foreach ($bx in @($x, ($x + $w - 6))) { Rect $bx ($y + $topH) 6 $h $DARK; Rect $bx ($y + $topH) 1 $h $OUT; Rect ($bx + 1) ($y + $topH) 1 $h $HI; Rect ($bx + 5) ($y + $topH) 1 $h $OUT }
    Rect $x ($y + $topH + $h - 1) $w 1 $OUT
    # Nails in the battens.
    foreach ($bx in @(($x + 2), ($x + $w - 4))) { foreach ($ny in @(($y + $topH + 3), ($y + $topH + $h - 6))) { NailHead $bx $ny } }
    Rect ($x + 2) ($y + $topH + $h) ($w - 2) 3 (C '000000' 80)
}

function CrateTop([int]$x, [int]$y, [int]$w, [int]$h) {
    SeedAt @($x, $y, $w, $h, 8)
    # The open top: dark inside, straw packing.
    Rect $x $y $w $h $OUT
    Rect ($x + 2) ($y + 1) ($w - 4) ($h - 1) (C '1C1610')
    for ($i = 0; $i -lt 40; $i++) {
        $sx = $x + 3 + $rng.Next(0, $w - 8); $sy = $y + 2 + $rng.Next(0, $h - 3)
        Rect $sx $sy ($rng.Next(2, 5)) 1 $(if ($rng.Next(0, 2) -eq 0) { $STRAW0 } else { $STRAW1 })
    }
}

function Lid([int]$x, [int]$y, [int]$w, [int]$h, [double]$tilt) {
    # Boards running left to right, tilted by $tilt px per column.
    for ($c = 0; $c -lt $w; $c++) {
        $dy = [int][Math]::Round($c * $tilt)
        for ($r = 0; $r -lt $h; $r++) {
            $b = $r % 5
            $col = $(if ($b -eq 0) { $OUT } elseif ($b -eq 1) { $HI2 } elseif ($b -eq 4) { $DARK } else { $HI })
            if ($c -eq 0 -or $c -eq $w - 1 -or $r -eq $h - 1) { $col = $OUT }
            Px ($x + $c) ($y + $r + $dy) $col
        }
    }
}

function CrateCloseup([int]$ax, [int]$ay, [int]$aw, [int]$ah, [bool]$broken) {
    Rect $ax $ay $aw $ah (C '1E2A22')
    Strip $ax ($ay + $ah - 26) $aw 26 'grass'
    $w = 70; $h = 42; $topH = 12
    $x = $ax + [int](($aw - $w) / 2); $y = $ay + $ah - 18 - $h - $topH
    if ($broken) {
        CrateTop $x $y $w $topH
        CrateBox $x $y $w $h $topH
        # The lid knocked loose: slid off to the right, one end up on the rim.
        Lid ($x + 22) ($y - 10) 60 15 0.22
        # A board sprung free, lying in the grass, and two bent nails.
        Board ($x - 20) ($ay + $ah - 10) ($x + 8) ($ay + $ah - 13) 5
        Px ($x + 4) ($y + 1) $I2; Px ($x + 5) ($y) $I2; Px ($x + 6) ($y - 1) $I3
        Px ($x + 14) ($y + 1) $I2; Px ($x + 15) ($y) $I3
    } else {
        CrateBox $x $y $w $h $topH
        Lid $x $y $w $topH 0
        foreach ($nx in @(($x + 3), ($x + $w - 5))) { foreach ($ny in @(($y + 2), ($y + 7))) { NailHead $nx $ny } }
    }
}


$S0 = C '2A2222'; $S1 = C '43382E'; $S2 = C '606050'; $S3 = C '787868'; $S4 = C 'A1A181'
$PAINT = C 'CFD0A0'
$G0 = C '7E5E26'; $G1 = C 'C0A040'; $G2 = C 'F0E060'

$GLYPHS = @{
    'S' = @('111', '100', '111', '001', '111'); 'A' = @('010', '101', '111', '101', '101')
    'L' = @('100', '100', '100', '100', '111'); 'E' = @('111', '100', '110', '100', '111')
    'M' = @('10001', '11011', '10101', '10001', '10001'); 'T' = @('111', '010', '010', '010', '010')
    'O' = @('111', '101', '101', '101', '111'); 'W' = @('10001', '10001', '10101', '11011', '10001')
    'N' = @('1001', '1101', '1011', '1001', '1001'); ' ' = @('0', '0', '0', '0', '0')
}

# The painted letters as a set of (u, v) points on the board.
$script:letters = @{}
$u = 12
foreach ($ch in 'SALEM TOWN'.ToCharArray()) {
    $gl = $GLYPHS[[string]$ch]
    for ($r = 0; $r -lt 5; $r++) { for ($c = 0; $c -lt $gl[$r].Length; $c++) { if ($gl[$r][$c] -eq '1') { $script:letters["$($u + $c),$(6 + $r)"] = 1 } } }
    $u += $gl[0].Length + 1
}

$ARM_L = 104; $ARM_H = 17
$grainSeed = @{}
for ($i = 0; $i -lt 30; $i++) { $grainSeed["$($rng.Next(4, $ARM_L - 14)),$($rng.Next(3, $ARM_H - 3))"] = $rng.Next(3, 9) }

# The arm's colour at board coordinates (u along, v across), or $null.
function ArmPix([int]$u, [int]$v) {
    if ($u -lt 0 -or $u -ge $ARM_L -or $v -lt 0 -or $v -ge $ARM_H) { return $null }
    $axis = ($ARM_H - 1) / 2.0
    $tip = $ARM_L - 12
    if ($u -gt $tip) {
        $half = $axis * (1 - ($u - $tip) / 12.0)
        if ([Math]::Abs($v - $axis) -gt $half + 0.5) { return $null }
        if ([Math]::Abs($v - $axis) -gt $half - 0.6) { return $OUT }
    }
    if ($v -eq 0 -or $v -eq $ARM_H - 1 -or $u -eq 0) { return $OUT }
    if ($script:letters.ContainsKey("$u,$v")) { return $PAINT }
    if ($script:letters.ContainsKey("$u,$($v - 1)")) { return $DARK }
    if ($v -eq 1) { return $HI2 }
    if ($v -eq 2) { return $HI }
    if ($v -ge $ARM_H - 3) { return $DARK }
    foreach ($k in $grainSeed.Keys) {
        $p = $k.Split(','); $gu = [int]$p[0]; $gv = [int]$p[1]
        if ($v -eq $gv -and $u -ge $gu -and $u -lt $gu + $grainSeed[$k]) { return $DARK }
    }
    return $MID
}

# Draw the arm hinged at (hx, hy), its top-left corner, turned by ang radians.
function Arm([int]$hx, [int]$hy, [double]$ang) {
    $ca = [Math]::Cos($ang); $sa = [Math]::Sin($ang)
    for ($y = $hy - 10; $y -le $hy + $ARM_L; $y++) {
        for ($x = $hx - 4; $x -le $hx + $ARM_L + 2; $x++) {
            $dx = $x - $hx; $dy = $y - $hy
            $u = [int][Math]::Round($dx * $ca + $dy * $sa)
            $v = [int][Math]::Round(-$dx * $sa + $dy * $ca)
            $col = ArmPix $u $v
            if ($col -ne $null) { Px $x $y $col }
        }
    }
}

# An iron knee brace from (x0, y0) to (x1, y1), two px thick.
function Brace([int]$x0, [int]$y0, [int]$x1, [int]$y1) {
    $n = [Math]::Max([Math]::Abs($x1 - $x0), [Math]::Abs($y1 - $y0))
    for ($i = 0; $i -le $n; $i++) {
        $x = [int][Math]::Round($x0 + ($x1 - $x0) * $i / $n); $y = [int][Math]::Round($y0 + ($y1 - $y0) * $i / $n)
        Px $x $y $I3; Px ($x + 1) $y $I1; Px $x ($y + 1) $I0
    }
    NailHead ($x0 - 1) ($y0 - 1); NailHead ($x1 - 1) ($y1 - 1)
}

function Footing([int]$x, [int]$y, [int]$w, [int]$h) {
    Rect $x $y $w $h $S3
    Rect $x $y $w 2 $S4; Rect $x $y 1 $h $S4
    Rect ($x + $w - 2) $y 2 $h $S2; Rect $x ($y + $h - 2) $w 2 $S2
    foreach ($l in @(@(4, 6, 9), @(12, 11, 7), @(3, 14, 6))) { Rect ($x + $l[0]) ($y + $l[1]) $l[2] 1 $S1 }
    Rect ($x - 1) ($y - 1) ($w + 2) 1 $S0; Rect ($x - 1) ($y + $h) ($w + 2) 1 $S0
    Rect ($x - 1) $y 1 $h $S0; Rect ($x + $w) $y 1 $h $S0
}

function SignCloseup([int]$ax, [int]$ay, [int]$aw, [int]$ah, [bool]$broken) {
    Rect $ax $ay $aw $ah (C '1E2A22')
    Ground $ax ($ay + $ah - 16) $aw 16
    $px = $ax + 36; $pw = 12
    $foot = $ay + $ah - 10
    Footing ($px - 5) ($foot - 18) ($pw + 10) 16
    Post $px ($ay + 4) ($foot - 18) $pw
    # The iron collar where the post meets the footing.
    Rect ($px - 1) ($foot - 21) ($pw + 2) 3 $I1; Rect ($px - 1) ($foot - 21) ($pw + 2) 1 $I3
    $hx = $px + $pw; $hy = $ay + 12
    if ($broken) {
        Arm $hx $hy 0.42
        # The brace has come away from the arm and hangs from its lower nail.
        Brace ($px + $pw + 1) ($hy + 30) ($px + $pw + 6) ($hy + 50)
        # A nail pulled half out of the post.
        Rect ($px + $pw) ($hy + 4) 4 1 $I2; Rect ($px + $pw + 4) ($hy + 3) 1 3 $I3
    } else {
        Arm $hx $hy 0
        Brace ($px + $pw + 1) ($hy + 30) ($px + $pw + 26) ($hy + $ARM_H - 1)
        NailHead ($hx + 2) ($hy + 3); NailHead ($hx + 2) ($hy + $ARM_H - 5)
    }
}

# --- the game: the approved bar, peg and gold zone, with a plumb bob in place
# of the windlass wheel --------------------------------------------------------

function PlumbBob([int]$x, [int]$y) {
    Rect ($x - 2) ($y - 22) 5 2 $I1; Rect ($x - 2) ($y - 22) 5 1 $I3
    Rect $x ($y - 20) 1 14 (C 'E1CDA5')
    $rows = @(1, 3, 5, 5, 5, 3, 1)
    for ($r = 0; $r -lt $rows.Count; $r++) {
        $w = $rows[$r]; $sx = $x - [int](($w - 1) / 2)
        for ($c = 0; $c -lt $w; $c++) { Px ($sx + $c) ($y - 6 + $r) $(if ($c -eq 0 -or $c -eq $w - 1) { $G0 } elseif ($c -eq 1 -and $r -lt 4) { $G2 } else { $G1 }) }
    }
}



# --- write the pieces -----------------------------------------------------------

New-Item -ItemType Directory -Force $Dest | Out-Null

# The pictures, broken and mended, one stage px per art px, 176 wide.
foreach ($broken in @($true, $false)) {
    $tag = $(if ($broken) { 'broken' } else { 'mended' })
    NewCanvas 176 72; CloseupFence 0 0 176 72 $broken; SavePng "fence-$tag"
    NewCanvas 176 96; WellCloseup 0 0 176 96 $broken; SavePng "well-$tag"
    NewCanvas 176 88; ShopCloseup 0 0 176 88 $broken; SavePng "shop-$tag"
    NewCanvas 176 88; CrateCloseup 0 0 176 88 $broken; SavePng "crate-$tag"
    NewCanvas 176 88; SignCloseup 0 0 176 88 $broken; SavePng "sign-$tag"
}

# The road comes in parts: repair_stage.gd shortens the trunk a section per
# round, puts a cut face on its end and stacks a round on the verge.
# ROAD_* in repair_stage.gd mirror these numbers.
$roadX = 56; $roadW = 64; $logY = 34; $logH = 16; $trunkL = 30; $trunkR = 146
NewCanvas 176 88; RoadGround 0 0 176 88 $roadX $roadW; Stump 148 ($logY - 2); SavePng 'road-ground'

NewCanvas 176 88
Crown 40 $logY $logH
LogBody $trunkL $trunkR $logY $logH
# Where it broke from the stump: a splintered end.
$teeth = @(1, 3, 2, 5, 3, 2, 4, 6, 3, 2, 4, 1, 3, 2, 1, 2)
for ($r = 0; $r -lt $logH; $r++) {
    for ($j = 1; $j -le $teeth[$r]; $j++) { Px ($trunkR + $j) ($logY + $r) $(if ($r -lt 3) { $E2 } else { $B3 }) }
    Px ($trunkR + $teeth[$r] + 1) ($logY + $r) $B0
}
SavePng 'road-trunk'

NewCanvas 11 16; CutFace 5 0 16; SavePng 'road-face'
NewCanvas 16 14; LogBody 4 15 0 12; CutFace 4 0 12; SavePng 'road-round'

# Cleared: the crown dragged onto the left verge as a brush pile.
NewCanvas 176 88
SeedAt @(88, 66)
Limb 46 66 ([Math]::PI + 0.2) 28 6 2
Limb 44 72 ([Math]::PI - 0.25) 30 6 2
Limb 40 60 ([Math]::PI + 0.6) 22 5 2
SavePng 'road-brush'

# Hammer game: the board, a nail (head over an 8 px shaft, drawn rising by
# region), a driven head, and the hammer's swing: one frame per angle in
# $HAMMER_ANGLES, level (the strike) first, the grip at the same place in each.
NewCanvas 152 14; Plank 0 0 152 14; SavePng 'plank'
NewCanvas 7 11; Nail 3 11 8; SavePng 'nail'
NewCanvas 7 2; NailDriven 3 0; SavePng 'nail-driven'
NewCanvas ($HAMMER_FRAME_W * $HAMMER_ANGLES.Count) $HAMMER_FRAME_H
for ($f = 0; $f -lt $HAMMER_ANGLES.Count; $f++) {
    $fx = $f * $HAMMER_FRAME_W
    DrawMapTurned $HAMMER $HAMMER_GRIP_X $HAMMER_GRIP_Y $HAMMER_ANGLES[$f] ($fx + $HAMMER_FRAME_GRIP_X) $HAMMER_FRAME_GRIP_Y $fx 0 $HAMMER_FRAME_W $HAMMER_FRAME_H
}
SavePng 'hammer'

# Windlass game: the bar (a 144 x 9 beam with iron caps; origin 2 px left of
# and 2 px above the bar), the peg, the wheel turned through eight notches, and
# the signpost's plumb bob.
NewCanvas 148 11
Plank 2 1 144 9
foreach ($ex in @(0, 144)) { Rect $ex 0 4 11 $I1; Rect ($ex + 1) 1 1 9 $I3 }
SavePng 'bar'
NewCanvas 5 22; Peg 2 10; SavePng 'peg'
NewCanvas 184 23
for ($f = 0; $f -lt 8; $f++) { Wheel (11 + 23 * $f) 11 ($f * [Math]::PI / 4) }
SavePng 'wheel'
NewCanvas 5 23; PlumbBob 2 22; SavePng 'plumb'

# Saw game: the log end-on (the receding body, then the sawn face; the face's
# centre is at (19, 27) in the back and (19, 19) in the face), the blade.
NewCanvas 48 47; LogBack 19 27 19 8; SavePng 'log-back'
NewCanvas 39 39; Disc 19 19 19 $false; SavePng 'log-face'
NewCanvas 68 10; SawBlade 0 2 68; SavePng 'saw'

"wrote $((Get-ChildItem $Dest -Filter *.png).Count) pieces to $((Resolve-Path $Dest).Path)"

