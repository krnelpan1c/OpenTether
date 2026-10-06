package org.opentether.app.ui.theme

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialExpressiveTheme
import androidx.compose.material3.MotionScheme
import androidx.compose.material3.Shapes
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp

// Brand palette (teal) used when dynamic colour is unavailable.
private val LightColors: ColorScheme = lightColorScheme(
    primary = Color(0xFF00696B),
    onPrimary = Color(0xFFFFFFFF),
    primaryContainer = Color(0xFF9CF1F2),
    onPrimaryContainer = Color(0xFF004F51),
    secondary = Color(0xFF4A6363),
    onSecondary = Color(0xFFFFFFFF),
    secondaryContainer = Color(0xFFCCE8E8),
    onSecondaryContainer = Color(0xFF324B4B),
    tertiary = Color(0xFF7B4E7F),
    onTertiary = Color(0xFFFFFFFF),
    tertiaryContainer = Color(0xFFFFD6FE),
    onTertiaryContainer = Color(0xFF613766),
    error = Color(0xFFBA1A1A),
    errorContainer = Color(0xFFFFDAD6),
    onErrorContainer = Color(0xFF93000A),
    background = Color(0xFFF8FAF8),
    surface = Color(0xFFF8FAF8),
    surfaceContainerLowest = Color(0xFFFFFFFF),
    surfaceContainerLow = Color(0xFFF2F4F3),
    surfaceContainer = Color(0xFFECEEED),
    surfaceContainerHigh = Color(0xFFE6E9E7),
    surfaceContainerHighest = Color(0xFFE1E3E2),
)

private val DarkColors: ColorScheme = darkColorScheme(
    primary = Color(0xFF80D4D5),
    onPrimary = Color(0xFF003738),
    primaryContainer = Color(0xFF004F51),
    onPrimaryContainer = Color(0xFF9CF1F2),
    secondary = Color(0xFFB0CCCC),
    onSecondary = Color(0xFF1B3435),
    secondaryContainer = Color(0xFF324B4B),
    onSecondaryContainer = Color(0xFFCCE8E8),
    tertiary = Color(0xFFEBB5ED),
    onTertiary = Color(0xFF48204E),
    tertiaryContainer = Color(0xFF613766),
    onTertiaryContainer = Color(0xFFFFD6FE),
    error = Color(0xFFFFB4AB),
    errorContainer = Color(0xFF93000A),
    onErrorContainer = Color(0xFFFFDAD6),
    background = Color(0xFF101414),
    surface = Color(0xFF101414),
    surfaceContainerLowest = Color(0xFF0B0F0F),
    surfaceContainerLow = Color(0xFF191C1C),
    surfaceContainer = Color(0xFF1D2020),
    surfaceContainerHigh = Color(0xFF272B2B),
    surfaceContainerHighest = Color(0xFF323535),
)

private val baseType = Typography()

/** Expressive type: heavier display and headline weights for emphasis. */
val AppTypography = Typography(
    displayLarge = baseType.displayLarge.copy(fontWeight = FontWeight.Bold),
    displayMedium = baseType.displayMedium.copy(fontWeight = FontWeight.Bold),
    displaySmall = baseType.displaySmall.copy(fontWeight = FontWeight.SemiBold),
    headlineLarge = baseType.headlineLarge.copy(fontWeight = FontWeight.SemiBold),
    headlineMedium = baseType.headlineMedium.copy(fontWeight = FontWeight.SemiBold),
    headlineSmall = baseType.headlineSmall.copy(fontWeight = FontWeight.SemiBold),
    titleLarge = baseType.titleLarge.copy(fontWeight = FontWeight.SemiBold),
    titleMedium = baseType.titleMedium.copy(fontWeight = FontWeight.SemiBold),
    titleSmall = baseType.titleSmall,
    bodyLarge = baseType.bodyLarge,
    bodyMedium = baseType.bodyMedium,
    bodySmall = baseType.bodySmall,
    labelLarge = baseType.labelLarge.copy(fontWeight = FontWeight.SemiBold),
    labelMedium = baseType.labelMedium,
    labelSmall = baseType.labelSmall,
)

/** Monospace style for commands and codes. */
val CodeFont = FontFamily.Monospace

private val AppShapes = Shapes(
    extraSmall = androidx.compose.foundation.shape.RoundedCornerShape(8.dp),
    small = androidx.compose.foundation.shape.RoundedCornerShape(12.dp),
    medium = androidx.compose.foundation.shape.RoundedCornerShape(20.dp),
    large = androidx.compose.foundation.shape.RoundedCornerShape(28.dp),
    extraLarge = androidx.compose.foundation.shape.RoundedCornerShape(36.dp),
)

@Composable
fun OpenTetherTheme(
    darkTheme: Boolean = isSystemInDarkTheme(),
    dynamicColor: Boolean = true,
    content: @Composable () -> Unit,
) {
    val colors = when {
        dynamicColor && Build.VERSION.SDK_INT >= Build.VERSION_CODES.S -> {
            val context = LocalContext.current
            if (darkTheme) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        }
        darkTheme -> DarkColors
        else -> LightColors
    }
    MaterialExpressiveTheme(
        colorScheme = colors,
        motionScheme = MotionScheme.expressive(),
        shapes = AppShapes,
        typography = AppTypography,
        content = content,
    )
}
