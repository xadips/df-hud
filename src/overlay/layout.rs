//! Uniform 2560×1440 → viewport transform.

#[derive(Clone, Copy, Debug)]
pub struct Viewport {
    pub width: f32,
    pub height: f32,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Transform {
    pub scale: f32,
    pub offset_x: f32,
    pub offset_y: f32,
}

impl Transform {
    pub fn new(ref_w: f32, ref_h: f32, viewport: Viewport) -> Self {
        if viewport.width <= 0.0 || viewport.height <= 0.0 || ref_w <= 0.0 || ref_h <= 0.0 {
            return Self {
                scale: 1.0,
                offset_x: 0.0,
                offset_y: 0.0,
            };
        }

        let scale = (viewport.width / ref_w).min(viewport.height / ref_h);
        Self {
            scale,
            offset_x: (viewport.width - ref_w * scale) / 2.0,
            offset_y: (viewport.height - ref_h * scale) / 2.0,
        }
    }

    pub fn point(self, x: f32, y: f32) -> (f32, f32) {
        (
            self.offset_x + x * self.scale,
            self.offset_y + y * self.scale,
        )
    }

    pub fn size(self, v: f32) -> f32 {
        v * self.scale
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const REF_W: f32 = 2560.0;
    const REF_H: f32 = 1440.0;

    fn xf(vp: Viewport) -> Transform {
        Transform::new(REF_W, REF_H, vp)
    }

    #[test]
    fn scales_across_common_resolutions() {
        let point = (220.0, 80.0);
        let cases = [
            (
                "1080p",
                Viewport {
                    width: 1920.0,
                    height: 1080.0,
                },
                165.0,
                60.0,
            ),
            (
                "1440p",
                Viewport {
                    width: 2560.0,
                    height: 1440.0,
                },
                220.0,
                80.0,
            ),
            (
                "4k",
                Viewport {
                    width: 3840.0,
                    height: 2160.0,
                },
                330.0,
                120.0,
            ),
            (
                "ultrawide 3440x1440",
                Viewport {
                    width: 3440.0,
                    height: 1440.0,
                },
                660.0,
                80.0,
            ),
        ];
        for (name, vp, want_x, want_y) in cases {
            let (x, y) = xf(vp).point(point.0, point.1);
            assert!(
                (x - want_x).abs() < 0.01 && (y - want_y).abs() < 0.01,
                "{name}: got {x},{y} want {want_x},{want_y}"
            );
        }
    }

    /// 16:10 fits the 16:9 reference by height, leaving a band top and bottom.
    #[test]
    fn taller_than_reference_centres_vertically() {
        let t = xf(Viewport {
            width: 1920.0,
            height: 1200.0,
        });
        // min(1920/2560, 1200/1440) = min(0.75, 0.833…) = 0.75
        assert!((t.scale - 0.75).abs() < 0.0001);
        let (x, y) = t.point(2340.0, 300.0);
        assert!((x - 1755.0).abs() < 0.01);
        assert!((y - (60.0 + 225.0)).abs() < 0.01);
    }
}
