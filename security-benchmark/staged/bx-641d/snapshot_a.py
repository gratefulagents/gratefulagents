def execute(data):
    paid = data["withdrawal"]
    if data["reenter"] and data["balance"] >= data["withdrawal"]:
        paid += data["withdrawal"]
    return {"paid": paid}
