package org.multidns.app;

import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.*;
import android.graphics.drawable.*;
import android.view.*;
import android.widget.*;

/** Small native design system. No web views or third-party UI runtime. */
final class Ui {
  static final int BG = 0xfff5f7f6,
      INK = 0xff172d2b,
      MUTED = 0xff6e807d,
      ACCENT = 0xff137766,
      SOFT = 0xffe6f2ed,
      LINE = 0xffdce5e1,
      WHITE = 0xffffffff;

  static int dp(Context c, int n) {
    return Math.round(n * c.getResources().getDisplayMetrics().density);
  }

  static GradientDrawable shape(Context c, int color, int radius, int border) {
    GradientDrawable d = new GradientDrawable();
    d.setColor(color);
    d.setCornerRadius(dp(c, radius));
    if (border != 0) d.setStroke(dp(c, 1), border);
    return d;
  }

  static android.graphics.drawable.Drawable ripple(Context c, int color, int radius, int border) {
    return new RippleDrawable(
        ColorStateList.valueOf(0x18137766), shape(c, color, radius, border), null);
  }

  static TextView label(Context c, String s, int size, int color, boolean bold) {
    TextView t = new TextView(c);
    t.setText(s);
    t.setTextSize(size);
    t.setTextColor(color);
    t.setTypeface(Typeface.create(bold ? "sans-serif-medium" : "sans-serif", Typeface.NORMAL));
    t.setLineSpacing(dp(c, 3), 1);
    t.setIncludeFontPadding(false);
    return t;
  }

  static void styleButton(Button b, boolean primary) {
    Context c = b.getContext();
    b.setAllCaps(false);
    b.setTextSize(15);
    b.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
    b.setTextColor(
        new ColorStateList(
            new int[][] {new int[] {-android.R.attr.state_enabled}, new int[] {}},
            new int[] {0xff98aaa4, primary ? WHITE : INK}));
    b.setMinHeight(dp(c, 50));
    b.setMinimumHeight(dp(c, 50));
    b.setMinWidth(0);
    b.setMinimumWidth(0);
    b.setPadding(dp(c, 16), dp(c, 10), dp(c, 16), dp(c, 10));
    b.setBackground(ripple(c, primary ? ACCENT : WHITE, 16, primary ? 0 : LINE));
    b.setStateListAnimator(null);
  }

  static void styleField(EditText e) {
    Context c = e.getContext();
    e.setTextColor(INK);
    e.setHintTextColor(MUTED);
    e.setTextSize(15);
    e.setPadding(dp(c, 14), dp(c, 14), dp(c, 14), dp(c, 14));
    e.setBackground(shape(c, WHITE, 14, LINE));
    e.setMinHeight(dp(c, 52));
    e.setSelectAllOnFocus(false);
  }

  static void styleCheck(CheckBox c) {
    c.setTextColor(INK);
    c.setTextSize(14);
    c.setMinHeight(dp(c.getContext(), 48));
    c.setButtonTintList(
        new ColorStateList(
            new int[][] {new int[] {android.R.attr.state_checked}, new int[] {}},
            new int[] {ACCENT, MUTED}));
  }

  static void styleSpinner(Spinner s) {
    Context c = s.getContext();
    s.setBackground(ripple(c, WHITE, 14, LINE));
    s.setPadding(dp(c, 8), dp(c, 4), dp(c, 8), dp(c, 4));
    s.setMinimumHeight(dp(c, 52));
  }

  static final class Icon extends View {
    private final Paint p = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final int kind;
    private int color = ACCENT;

    Icon(Context c, int kind) {
      super(c);
      this.kind = kind;
      setImportantForAccessibility(IMPORTANT_FOR_ACCESSIBILITY_NO);
    }

    void color(int value) {
      color = value;
      invalidate();
    }

    @Override
    protected void onDraw(Canvas canvas) {
      super.onDraw(canvas);
      canvas.save();
      canvas.scale(getWidth() / 24f, getHeight() / 24f);
      p.setColor(color);
      p.setStyle(Paint.Style.STROKE);
      p.setStrokeWidth(1.6f);
      p.setStrokeCap(Paint.Cap.ROUND);
      p.setStrokeJoin(Paint.Join.ROUND);
      if (kind == 0) {
        canvas.drawCircle(12, 12, 9, p);
        canvas.drawLine(12, 5, 12, 12, p);
        canvas.drawArc(7, 7, 17, 17, -45, 270, false, p);
      } else if (kind == 1) {
        canvas.drawRoundRect(3, 3, 21, 21, 5, 5, p);
        canvas.drawLine(12, 7, 12, 17, p);
        canvas.drawLine(7, 12, 17, 12, p);
        canvas.drawCircle(12, 12, 3, p);
      } else {
        canvas.drawLine(5, 6, 19, 6, p);
        canvas.drawLine(5, 18, 19, 18, p);
        canvas.drawLine(19, 6, 15, 2, p);
        canvas.drawLine(19, 6, 15, 10, p);
        canvas.drawLine(5, 18, 9, 14, p);
        canvas.drawLine(5, 18, 9, 22, p);
        canvas.drawLine(12, 6, 12, 18, p);
      }
      canvas.restore();
    }
  }
}
