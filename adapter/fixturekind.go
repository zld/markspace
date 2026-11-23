package adapter

// Fixture-facing kind accessors so engine code does not depend on internal
// byte constants of the metadata container.
func KindSubtitleForFixture() byte { return kindSubtitle }
func KindChapterForFixture() byte  { return kindChapter }
func KindID3ForFixture() byte      { return kindID3 }
func KindEmsgForFixture() byte     { return kindEmsg }
func KindSEIForFixture() byte      { return kindSEI }
