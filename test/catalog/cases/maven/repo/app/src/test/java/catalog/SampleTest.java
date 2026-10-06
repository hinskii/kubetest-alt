package catalog;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;

class SampleTest {
    @Test
    void passes() {
        assertEquals(2, 1 + 1);
    }

    @Test
    void failsOnPurpose() {
        assertEquals(3, 1 + 1, "deliberate failure: the catalog e2e expects one red test");
    }
}
