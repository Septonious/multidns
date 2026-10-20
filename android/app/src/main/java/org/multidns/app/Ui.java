package org.multidns.app;

import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.*;
import android.graphics.drawable.*;
import android.view.*;
import android.widget.*;

/** Small native design system. No web views or third-party UI runtime. */
final class Ui {
  static final int BG = 0xff101617,
      INK = 0xffe7efed,
      MUTED = 0xffa5b7b1,
      ACCENT = 0xff78dac0,
      SOFT = 0xff173c35,
      LINE = 0xff304341,
      SURFACE = 0xff192224,
      ON_ACCENT = 0xff09231d;

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
        ColorStateList.valueOf(0x2878dac0), shape(c, color, radius, border), null);
  }

  static TextView label(Context c, String s, int size, int color, boolean bold) {
    TextView t = new TextView(c);
    t.setText(s);
    t.setTextSize(size);
    t.setTextColor(color);
    t.setTypeface(Typeface.create(bold ? "sans-serif-medium" : "sans-serif", Typeface.NORMAL));
    t.setLineSpacing(dp(c, 2), 1);
    t.setIncludeFontPadding(false);
    return t;
  }

  static void styleButton(Button b, boolean primary) {
    Context c = b.getContext();
    b.setAllCaps(false);
    b.setLetterSpacing(0);
    b.setTextSize(15);
    b.setTypeface(Typeface.create("sans-serif-medium", Typeface.NORMAL));
    b.setTextColor(
        new ColorStateList(
            new int[][] {new int[] {-android.R.attr.state_enabled}, new int[] {}},
            new int[] {0xff657a74, primary ? ON_ACCENT : INK}));
    b.setMinHeight(dp(c, 48));
    b.setMinimumHeight(dp(c, 48));
    b.setMinWidth(0);
    b.setMinimumWidth(0);
    b.setPadding(dp(c, 16), dp(c, 10), dp(c, 16), dp(c, 10));
    b.setBackground(ripple(c, primary ? ACCENT : SURFACE, 16, primary ? 0 : LINE));
    b.setStateListAnimator(null);
  }

  static void styleField(EditText e) {
    Context c = e.getContext();
    e.setTextColor(INK);
    e.setHintTextColor(MUTED);
    e.setTextSize(15);
    e.setPadding(dp(c, 12), dp(c, 12), dp(c, 12), dp(c, 12));
    e.setBackground(shape(c, SURFACE, 12, LINE));
    e.setMinHeight(dp(c, 48));
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
    s.setBackgroundResource(R.drawable.bg_spinner);
    s.setPadding(dp(c, 12), 0, dp(c, 36), 0);
    s.setMinimumHeight(dp(c, 48));
    s.setPopupBackgroundDrawable(shape(c, SURFACE, 12, LINE));
  }
}
