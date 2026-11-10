package main

import (
	"math"
	"testing"
)

func rectW(r winRect) float64 { return float64(r.Right - r.Left) }
func rectH(r winRect) float64 { return float64(r.Bottom - r.Top) }

func checkAspect(t *testing.T, r winRect, label string) {
	t.Helper()
	w, h := rectW(r), rectH(r)
	if got := w / h; math.Abs(got-windowAspect) > 0.01 {
		t.Fatalf("%s: 宽高比不对，%vx%v = %.4f，期望 %.4f", label, w, h, got, windowAspect)
	}
}

func TestFitRectToAspectRightEdge(t *testing.T) {
	// 拖右边：只改宽度，高度跟着比例走，Left/Top 不动
	r := fitRectToAspect(winRect{0, 0, 1600, 900}, 2, 1220, 820, 1000, 672)
	checkAspect(t, r, "right-edge")
	if r.Left != 0 || r.Top != 0 {
		t.Fatalf("拖右边不该移动左上角：%+v", r)
	}
	if math.Abs(rectW(r)-1600) > 1 {
		t.Fatalf("宽度应保持 1600，实际 %v", rectW(r))
	}
}

func TestFitRectToAspectBottomEdge(t *testing.T) {
	// 拖下边：只改高度，宽度跟着比例走
	r := fitRectToAspect(winRect{0, 0, 1220, 900}, 4, 1220, 820, 1000, 672)
	checkAspect(t, r, "bottom-edge")
	if math.Abs(rectH(r)-900) > 1 {
		t.Fatalf("高度应保持 900，实际 %v", rectH(r))
	}
}

func TestFitRectToAspectLeftEdgeAnchor(t *testing.T) {
	// 拖左边：Right 固定，Left 往左推
	r := fitRectToAspect(winRect{300, 0, 600, 300}, 1, 1220, 820, 1000, 672)
	checkAspect(t, r, "left-edge")
	if r.Right != 600 {
		t.Fatalf("拖左边时 Right 应保持 600，实际 %v", r.Right)
	}
	if rectW(r) < 1000 {
		t.Fatalf("不该小于最小宽度，实际 %v", rectW(r))
	}
}

func TestFitRectToAspectMinSize(t *testing.T) {
	// 角落拖小：必须停在最小尺寸之上
	for _, edge := range []uintptr{5, 6, 7, 8} {
		r := fitRectToAspect(winRect{100, 100, 500, 300}, edge, 1220, 820, 1000, 672)
		checkAspect(t, r, "corner")
		if rectW(r) < 999 || rectH(r) < 671 {
			t.Fatalf("edge=%d 小于最小尺寸：%vx%v", edge, rectW(r), rectH(r))
		}
	}
}

func TestFitRectToAspectKeepsGrow(t *testing.T) {
	// 放大：比例仍然成立
	r := fitRectToAspect(winRect{0, 0, 2440, 1000}, 8, 1220, 820, 1000, 672)
	checkAspect(t, r, "grow")
	if rectW(r) < 2440 {
		t.Fatalf("放大后宽度不应被压缩：%v", rectW(r))
	}
}
