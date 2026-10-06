import io.gatling.core.Predef._
import io.gatling.http.Predef._

class FixtureSimulation extends Simulation {
  val httpProtocol = http.baseUrl("http://target:8000")
  val scn = scenario("fixture").repeat(5) {
    exec(http("root").get("/")).exec(http("missing").get("/missing"))
  }
  setUp(scn.inject(atOnceUsers(2))).protocols(httpProtocol)
}
